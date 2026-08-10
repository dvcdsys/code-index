package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/systray"
)

// The menu bar UI.
//
// systray.Run takes over the calling goroutine and must own the main one — on
// macOS the status item lives on the AppKit main thread. Everything else here
// runs in goroutines and only touches systray through its setters, which are
// safe to call from anywhere.
//
// Layout follows the platform rather than inventing one: disabled rows carrying
// state with a coloured indicator, then actions, then a checkbox for the one
// setting, then Quit. Every row is width-capped (see maxRowRunes) because an
// NSMenu is as wide as its widest row.

type menu struct {
	bundle bundle
	poll   *poller
	stop   chan struct{}

	statusItem     *systray.MenuItem
	embeddingsItem *systray.MenuItem
	modelItem      *systray.MenuItem
	startStopItem  *systray.MenuItem
	dashboardItem  *systray.MenuItem
	autostartItem  *systray.MenuItem
	networkItem    *systray.MenuItem
	resetPWItem    *systray.MenuItem
	updateItem     *systray.MenuItem

	updater *updater

	// detail holds the submenu rows under the server row. They are created
	// once and retitled on every render: systray has no way to remove an item,
	// so rebuilding the submenu per update would grow it without bound.
	detail [detailRows]*systray.MenuItem
}

// detailRows is the fixed number of submenu slots. Rows with nothing to say are
// hidden rather than left blank.
const detailRows = 7

func runMenu(b bundle, u *updater) {
	m := &menu{bundle: b, poll: newPoller(), stop: make(chan struct{}), updater: u}
	systray.Run(m.onReady, m.onExit)
}

// setProgress puts a word next to the menu bar icon while something slow is
// happening, and clears it when passed "".
//
// The alternative was a modal dialog, and it is the wrong shape: a runtime
// download takes tens of seconds, during which a menu bar app that shows nothing
// reads as hung and one that blocks with an alert cannot be dismissed. AppKit
// draws this text beside the template icon, so it is visible without being in
// the way.
func (m *menu) setProgress(msg string) {
	systray.SetTitle(msg)
	if msg != "" {
		logf("%s", msg)
	}
}

func (m *menu) onReady() {
	if icon, err := os.ReadFile(filepath.Join(m.bundle.Resources, "cixTemplate@2x.png")); err == nil {
		// SetTemplateIcon, not SetIcon: macOS recolours a template image for
		// dark mode, for a tinted menu bar and for the pressed state, using
		// only its alpha channel. A coloured icon here is a smudge at 18 px and
		// unreadable in dark mode.
		systray.SetTemplateIcon(icon, icon)
	} else {
		systray.SetTitle("cix")
	}
	// No tooltips anywhere, including on the status item itself. AppKit's are
	// not usable here: once any one of them has appeared, every subsequent one
	// shows with no delay at all, and they are positioned against the element
	// rather than the pointer. Neither has an API — changing either means
	// giving every row a custom NSView with its own tracking area, i.e. writing
	// the menu in Objective-C instead of using systray. Everything a tooltip
	// would have said is in the details submenu, where the timing and placement
	// are the system's own and correct.
	//
	// The empty second argument to AddMenuItem is that tooltip. Leave it empty.

	// The server row is the one enabled row in the status group, because it
	// carries the details submenu — a parent has to be enabled for macOS to
	// open its submenu. The disclosure arrow reads as "there is more here"
	// rather than "this does something", which is what it is.
	m.statusItem = systray.AddMenuItem("cix-server: …", "")
	for i := range m.detail {
		m.detail[i] = m.statusItem.AddSubMenuItem("", "")
		m.detail[i].Disable()
	}

	m.embeddingsItem = systray.AddMenuItem("Embeddings: …", "")
	m.embeddingsItem.Disable()
	m.modelItem = systray.AddMenuItem("", "")
	m.modelItem.Disable()
	m.modelItem.Hide()

	systray.AddSeparator()
	m.startStopItem = systray.AddMenuItem("Start Server", "")
	m.dashboardItem = systray.AddMenuItem("Open Dashboard", "")

	systray.AddSeparator()
	m.autostartItem = systray.AddMenuItemCheckbox("Start at Login", "", false)
	m.networkItem = systray.AddMenuItemCheckbox("Allow Network Access", "", false)
	m.resetPWItem = systray.AddMenuItem("Reset Password…", "")

	systray.AddSeparator()
	m.updateItem = systray.AddMenuItem("Check for Updates…", "")

	systray.AddSeparator()
	// Spelled out rather than left to a tooltip. Quit here closes the menu bar
	// app and leaves the launchd agent running, which is the opposite of what
	// Quit means in most menu bar apps — a surprise worth 22 characters.
	quitItem := systray.AddMenuItem("Quit (server keeps running)", "")

	go m.poll.run(m.stop)
	go m.watch()

	// A quiet background check. It only speaks up when there is something to
	// offer, and it is throttled and ETag-cached, so an app left open all day
	// costs a handful of 304s.
	go m.checkForUpdates(false)

	go func() {
		for {
			select {
			case <-m.startStopItem.ClickedCh:
				go m.toggleServer()
			case <-m.dashboardItem.ClickedCh:
				go m.openDashboard()
			case <-m.autostartItem.ClickedCh:
				go m.toggleAutostart()
			case <-m.networkItem.ClickedCh:
				go m.toggleNetworkAccess()
			case <-m.resetPWItem.ClickedCh:
				go m.resetPasswordFlow()
			case <-m.updateItem.ClickedCh:
				go m.checkForUpdates(true)
			case <-quitItem.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func (m *menu) onExit() {
	close(m.stop)
}

// watch redraws the menu whenever the poller reports a change.
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
	m.statusItem.SetTitle(s.ServerLine())
	m.statusItem.SetIcon(dotPNG(s.ServerDot()))
	m.renderDetail(s)

	m.embeddingsItem.SetTitle(s.EmbeddingsLine())
	m.embeddingsItem.SetIcon(dotPNG(s.EmbeddingsDot()))

	if line := s.ModelLine(); line != "" {
		m.modelItem.SetTitle(line)
		// A transparent spacer, not a missing icon: AppKit indents a title by
		// its image width, so without one this row would start left of the two
		// above it and the group would read as ragged.
		m.modelItem.SetIcon(dotPNG(dotBlank))
		m.modelItem.Show()
	} else {
		m.modelItem.Hide()
	}

	switch {
	case !s.Managed:
		// Another installation owns the launchd label. Showing an enabled
		// Start button that would fight it — or worse, silently repoint it — is
		// the wrong behaviour; observing is useful, interfering is not.
		m.startStopItem.SetTitle("Start Server")
		m.startStopItem.Disable()
	case s.State == stateRunning:
		m.startStopItem.SetTitle("Stop Server")
		m.startStopItem.Enable()
	case s.State == stateStarting:
		m.startStopItem.SetTitle("Starting…")
		m.startStopItem.Disable()
	default:
		m.startStopItem.SetTitle("Start Server")
		m.startStopItem.Enable()
	}

	if s.State == stateRunning {
		m.dashboardItem.Enable()
	} else {
		m.dashboardItem.Disable()
	}

	if s.LocalOnly {
		m.networkItem.Uncheck()
	} else {
		m.networkItem.Check()
	}
	if s.Autostart {
		m.autostartItem.Check()
	} else {
		m.autostartItem.Uncheck()
	}

	if s.Managed {
		m.networkItem.Enable()
		m.autostartItem.Enable()
		m.resetPWItem.Enable()
	} else {
		// These live in files this app does not own — except the password
		// reset, which only needs the database and is therefore still useful
		// against an install-server.sh deployment.
		m.networkItem.Disable()
		m.autostartItem.Disable()
		m.resetPWItem.Enable()
	}
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
// it. `explicit` distinguishes the menu item from the background check: a
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
	systray.Quit()
}

// renderDetail fills the submenu under the server row. Slots with nothing to
// say are hidden, so the submenu never shows an empty line.
func (m *menu) renderDetail(s snapshot) {
	lines := [detailRows]string{
		s.DetailProcess(),
		s.DetailPort(),
		s.DetailNetwork(),
		s.DetailModel(),
		s.DetailVersion(),
		// What is installed, as opposed to what the running server reports. The
		// row above is empty whenever the server is not answering — which is
		// exactly when someone wants to know which server is on disk.
		runtimeSummary(),
		s.DetailManaged(),
	}
	for i, line := range lines {
		if line == "" {
			m.detail[i].Hide()
			continue
		}
		m.detail[i].SetTitle(line)
		m.detail[i].Show()
	}
}

func (m *menu) toggleServer() {
	s := m.poll.snapshotNow()
	if !s.Managed {
		return
	}

	var err error
	if s.State == stateRunning {
		err = stopServer()
	} else {
		// Rewrite the wrapper before starting. It is generated, not edited, and
		// something else may have replaced it — install-server.sh most obviously.
		if err = writeLaunchdFiles(autostartEnabled()); err == nil {
			err = startServer()
		}
	}
	if err != nil {
		verb := "start"
		if s.State == stateRunning {
			verb = "stop"
		}
		_ = alert("cix", fmt.Sprintf("Could not %s the server.\n\n%v", verb, err))
	}
	m.poll.refresh()
}

// toggleNetworkAccess switches CIX_BIND_ADDR between loopback and all
// interfaces, then restarts the server so the change takes effect.
//
// Widening access asks first. Nothing else in this menu changes what the
// machine exposes to the network, and a checkbox is an easy thing to hit by
// accident; narrowing access needs no confirmation because it can only be safe.
func (m *menu) toggleNetworkAccess() {
	vars, err := readServerEnv()
	if err != nil {
		_ = alert("cix", "cix is not set up yet.")
		return
	}

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
			// Put the checkbox back: the click already toggled it visually.
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
