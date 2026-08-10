package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The menu bar UI — a custom panel, not an NSMenu.
//
// The AppKit half lives in panel_darwin.m and the look in panel.html; this
// file is the behaviour. It reacts to poller changes by pushing a fresh
// panelState into the webview, and to panel actions by running the same
// operations the old menu ran. The design brief that replaced the NSMenu is
// under the repo's design notes: status as a header instead of disabled rows,
// actions weighted by importance, toggles that read as toggles.

type menu struct {
	bundle bundle
	poll   *poller
	stop   chan struct{}

	updater *updater

	// busy is held while something is restarting the server, and it exists
	// because several of these actions are not independent.
	//
	// Launch at Login and Allow Network Access both restart the server, each
	// in its own goroutine, and each drives the same launchd label: one boots
	// the job out while the other is waiting for its pid to disappear and then
	// bootstraps it itself. Two bootstraps of one label leave either a failure
	// or two processes racing for the port — from the outside, a server that
	// went away and did not come back.
	//
	// Serialising them would only queue the second restart behind the first,
	// which is not what anyone clicking meant. Refusing the click, and greying
	// the control that would produce it, says what is actually happening.
	busy atomic.Bool

	// retireOnce drops the bootstrap password from server.env the first time
	// this process sees a running server. See retireBootstrapPassword.
	retireOnce sync.Once
}

// beginBusy claims the right to restart the server. False means something else
// already has it, and the caller must do nothing.
func (m *menu) beginBusy() bool {
	if !m.busy.CompareAndSwap(false, true) {
		logf("ignored a panel action: another server operation is already running")
		return false
	}
	m.render(m.poll.snapshotNow())
	return true
}

func (m *menu) endBusy() {
	m.busy.Store(false)
	m.poll.refresh()
}

func runMenu(b bundle, u *updater) {
	m := &menu{bundle: b, poll: newPoller(), stop: make(chan struct{}), updater: u}

	panelHooks.onReady = m.onReady
	panelHooks.onExit = m.onExit
	panelHooks.onAction = m.onAction

	// Never returns until quit; must own the main thread (see main_darwin.go).
	panelRun(filepath.Join(b.Resources, "cixTemplate.png"))
}

// setProgress puts a word next to the menu bar icon while something slow is
// happening, and clears it when passed "".
//
// The alternative was a modal dialog, and it is the wrong shape: a runtime
// download takes tens of seconds, during which a menu bar app that shows
// nothing reads as hung and one that blocks with an alert cannot be dismissed.
// AppKit draws this text beside the template icon, so it is visible without
// being in the way.
func (m *menu) setProgress(msg string) {
	panelSetTitle(msg)
	if msg != "" {
		logf("%s", msg)
	}
}

func (m *menu) onReady() {
	go m.poll.run(m.stop)
	go m.watch()

	// A quiet background check. It only speaks up when there is something to
	// offer, and it is throttled and ETag-cached, so an app left open all day
	// costs a handful of 304s.
	go m.checkForUpdates(false)
}

func (m *menu) onExit() {
	close(m.stop)
}

// onAction is the panel's dispatcher. Each handler blocks (dialogs, server
// restarts) and arrives on its own goroutine — see goPanelAction.
func (m *menu) onAction(a panelAction) {
	switch a.Action {
	case "opened":
		// The panel just became visible; answer with a fresh poll so the
		// uptime and status shown are seconds old, not up to a tick old. The
		// log line doubles as the proof-of-life for the whole ObjC bridge —
		// it only appears if the status item, the panel and the webview all
		// actually came up.
		logf("panel opened")
		m.poll.refresh()
	case "toggle-server":
		m.toggleServer()
	case "dashboard":
		m.openDashboard()
	case "toggle-network":
		m.toggleNetworkAccess()
	case "toggle-autostart":
		m.toggleAutostart()
	case "reset-password":
		m.resetPasswordFlow()
	case "check-updates":
		m.checkForUpdates(true)
	case "quit":
		panelQuit()
	default:
		logf("panel sent an unknown action %q", a.Action)
	}
}

// watch redraws the panel whenever the poller reports a change.
func (m *menu) watch() {
	for {
		select {
		case <-m.stop:
			return
		case <-m.poll.changed:
			m.render(m.poll.snapshotNow())
		}
	}
}

func (m *menu) render(s snapshot) {
	if s.State == stateRunning {
		// Bootstrap runs before the listener opens, so a server that answers is
		// a server whose admin account exists — and the password that seeded it
		// has no further use.
		m.retireOnce.Do(retireBootstrapPassword)
	}
	panelSetState(buildPanelState(s, m.busy.Load()))
}

// toggleAutostart flips RunAtLoad on the launchd agent.
//
// launchd reads a plist only when the job is bootstrapped, so the change needs
// a full unload/load cycle — which stops a running server. Rather than leave it
// down, or pretend the cost is zero, this restores whatever state the server
// was in.
func (m *menu) toggleAutostart() {
	s := m.poll.snapshotNow()
	if !s.Managed {
		return
	}
	if !m.beginBusy() {
		// The switch already flipped visually on the click; put it back.
		m.render(s)
		return
	}
	defer m.endBusy()
	enable := !s.Autostart

	if err := setAutostart(enable); err != nil {
		_ = alert("cix", fmt.Sprintf("Could not change the start-at-login setting.\n\n%v", err))
		m.render(m.poll.snapshotNow())
		return
	}

	// bootstrap does not start the job unless RunAtLoad fired, so a server that
	// was running before the reload has to be put back.
	if s.State == stateRunning {
		if err := startServer(); err != nil {
			_ = alert("cix", fmt.Sprintf("The setting was changed, but the server could not be restarted.\n\n%v", err))
		}
	}
	m.poll.refresh()
}

// checkForUpdates looks for a newer release and, if the user agrees, installs
// it. `explicit` distinguishes the footer link from the background check: a
// background check that finds nothing says nothing.
func (m *menu) checkForUpdates(explicit bool) {
	av := m.updater.check(explicit)
	if !av.any() {
		if explicit {
			_ = alert("cix is up to date", fmt.Sprintf(
				"You are running cix %s with %s.", displayVersion(), strings.ToLower(runtimeSummary())))
		}
		return
	}

	// The app and the server are separate releases and feel completely
	// different to update: the server restarts and the app never goes away, the
	// app closes and reopens while the server keeps running. Naming which one
	// is happening is the difference between an expected restart and an
	// unexplained one.
	var what, effect string
	switch {
	case av.App.Version == "":
		what = fmt.Sprintf("cix server %s is available. You are running %s.", av.Runtime.Version, currentRuntimeVersion())
		effect = "The server will be updated and restarted. This app stays open, and if the new server does not start, cix goes back to the current one."
	case av.Runtime.Version == "":
		what = fmt.Sprintf("cix %s is available. You are running %s.", av.App.Version, displayVersion())
		effect = "cix will close and reopen. The server keeps running throughout."
	default:
		what = fmt.Sprintf("cix %s and cix server %s are available.", av.App.Version, av.Runtime.Version)
		effect = "The server will be updated and restarted, then cix will close and reopen."
	}

	ok, err := ask("An update is available",
		fmt.Sprintf("%s\n\nEverything is downloaded and checked before anything is replaced. %s", what, effect),
		"Update Now", "Not Now")
	if err != nil || !ok {
		return
	}

	// "Is there a process", not "is it answering". A server still loading its
	// embedding model has a pid and no /health yet; treating that as stopped
	// would skip the shutdown and swap the runtime under a live cix-server,
	// leaving it with a llama sidecar from a different version.
	wasRunning := m.poll.snapshotNow().PID != 0

	// Claimed only now, not around the check: the check is HTTP and touches
	// nothing, while installing stops and starts the server like the toggles do.
	if !m.beginBusy() {
		_ = alert("cix is busy", "Another server operation is still running. Try the update again once it has finished.")
		return
	}
	defer m.endBusy()

	quit, err := m.updater.install(av, wasRunning, m.setProgress)
	if err != nil {
		logf("update failed: %v", err)
		_ = alert("Could not install the update", err.Error())
		m.poll.refresh()
		return
	}
	if !quit {
		// Server-only update: the app is not going anywhere, so it has to say
		// the update finished. The launcher path says nothing here because it
		// is about to disappear and reappear, which speaks for itself.
		m.poll.refresh()
		_ = alert("cix server updated", fmt.Sprintf("The cix server is now running %s.", av.Runtime.Version))
		return
	}

	// From here the swap helper owns the outcome: it waits for this process to
	// exit before moving anything, so quitting is the last required step.
	panelQuit()
}

func (m *menu) toggleServer() {
	s := m.poll.snapshotNow()
	if !s.Managed {
		return
	}
	if !m.beginBusy() {
		return
	}
	defer m.endBusy()

	if s.State == stateRunning {
		if err := stopServer(); err != nil {
			_ = alert("cix", fmt.Sprintf("Could not stop the server.\n\n%v", err))
		}
		m.poll.refresh()
		return
	}

	// A missing database is not something Start can fix. The server refuses to
	// boot without an admin account to create — correctly — and says so in a
	// log nobody has open, so the button appears to do nothing at all. Offer
	// the thing that would actually help.
	if needsFirstRun() {
		m.offerSetupAgain(
			"There is no cix database. If you deleted it, the server cannot start until an " +
				"administrator account is created again.\n\n" +
				"Setting up again creates a new account and a new, empty index. Anything that " +
				"was indexed before is already gone with the database.")
		m.poll.refresh()
		return
	}

	// Rewrite the wrapper before starting. It is generated, not edited, and
	// something else may have replaced it — install-server.sh most obviously.
	err := writeLaunchdFiles(autostartEnabled())
	if err == nil {
		err = startServer()
	}
	if err != nil {
		_ = alert("cix", fmt.Sprintf("Could not start the server.\n\n%v", err))
		return
	}

	// launchctl reports success for having spawned the process, not for the
	// process surviving. A server that exits on a configuration it cannot
	// accept leaves the panel saying "Stopped" and nothing else — which is how
	// a deleted database looked like a broken Start button.
	if detail := serverDiedOnStart(); detail != "" {
		logf("the server exited immediately after Start: %s", detail)
		// One refusal deserves better than its log text: no admin account. The
		// needsFirstRun check above cannot see this case, because the failed
		// start itself recreated an empty cix.db — the file exists, the
		// accounts do not. The server's own words are the reliable signal, so
		// route them to setup instead of printing them.
		if isBootstrapRefusal(detail) {
			m.offerSetupAgain(
				"The cix database exists but has no accounts — this is what deleting the data " +
					"directory looks like after a start attempt recreates an empty database.\n\n" +
					"The server will not start until an administrator account is created again. " +
					"Setting up again creates a new account and a new, empty index.")
			m.poll.refresh()
			return
		}
		_ = alert("The server stopped straight away", detail+
			"\n\nThe full log is in ~/.cix/logs/cix-server.err.")
	}
	m.poll.refresh()
}

// offerSetupAgain proposes re-running the setup wizard and runs it on consent.
//
// Shared by the two ways a gutted installation shows itself: the database file
// is missing outright (needsFirstRun), or it exists, freshly recreated and
// empty, and the server refused to start against it (isBootstrapRefusal).
func (m *menu) offerSetupAgain(message string) {
	ok, err := confirm("Set cix up again?", message, "Set Up")
	if err != nil || !ok {
		return
	}
	if err := runFirstRun(m.updater); err != nil && !errors.Is(err, errCancelled) {
		logf("re-running setup failed: %v", err)
		_ = alert("Setup failed", fmt.Sprintf("cix could not set itself up again.\n\n%v", err))
	}
}

// toggleNetworkAccess switches CIX_BIND_ADDR between loopback and all
// interfaces, then restarts the server so the change takes effect.
//
// Widening access asks first. Nothing else in this panel changes what the
// machine exposes to the network, and a switch is an easy thing to hit by
// accident; narrowing access needs no confirmation because it can only be safe.
func (m *menu) toggleNetworkAccess() {
	vars, err := readServerEnv()
	if err != nil {
		_ = alert("cix", "cix is not set up yet.")
		return
	}
	if !m.beginBusy() {
		m.render(m.poll.snapshotNow())
		return
	}
	defer m.endBusy()

	wasRunning := m.poll.snapshotNow().State == stateRunning
	local := isLocalOnly(vars)

	if local {
		ok, err := confirm("Allow access from your network?",
			fmt.Sprintf("cix will accept connections from any machine that can reach this Mac on port %d, "+
				"instead of only from this Mac.\n\n"+
				"Accounts and API keys still apply — this does not disable authentication — but the login page "+
				"and the API become reachable from your network.\n\n"+
				"The server will restart.", serverPort(vars)),
			"Allow")
		if err != nil || !ok {
			// Put the switch back: the click already toggled it visually.
			m.render(m.poll.snapshotNow())
			return
		}
		vars["CIX_BIND_ADDR"] = bindAllInterfaces
	} else {
		vars["CIX_BIND_ADDR"] = bindLocalOnly
	}

	if err := writeServerEnv(vars); err != nil {
		_ = alert("cix", fmt.Sprintf("Could not save the setting.\n\n%v", err))
		m.render(m.poll.snapshotNow())
		return
	}

	// The bind address is read once, when the process starts, so the setting
	// means nothing until the server is restarted. Saying "saved" without doing
	// that would be a lie the user only discovers when it does not work.
	if wasRunning {
		if err := restartServer(); err != nil {
			_ = alert("cix", fmt.Sprintf("The setting was saved, but the server could not be restarted.\n\n%v", err))
		}
	}
	m.poll.refresh()
}

// restartServer stops the server, waits for the process to actually go away,
// and starts it again. The wait matters: kickstarting while the old process
// still holds the listening socket produces a server that exits immediately
// with "address already in use".
func restartServer() error {
	if err := stopServer(); err != nil {
		return err
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if launchdPID() == 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return startServer()
}

func (m *menu) openDashboard() {
	vars, err := readServerEnv()
	if err != nil {
		_ = alert("cix", "cix is not set up yet.")
		return
	}
	if err := exec.Command("open", dashboardURL(vars)).Run(); err != nil {
		_ = alert("cix", fmt.Sprintf("Could not open the dashboard.\n\n%v", err))
	}
}
