package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Password recovery from the menu.
//
// This drives `cix-server -reset-password <email>`, which opens the SQLite
// database directly — no HTTP, no auth — so possession of the database file is
// the authorization boundary. The server does NOT need to be stopped for it.

// resetPasswordFlow prompts for an address, runs the reset, and shows the
// generated password.
func (m *menu) resetPasswordFlow() {
	vars, err := readServerEnv()
	if err != nil {
		_ = alert("cix", "cix is not set up yet.")
		return
	}

	email, err := prompt("Reset password",
		"Enter the email address of the account to reset.\n\n"+
			"A new temporary password will be generated. Any active sessions for the "+
			"account are signed out, and the next login forces a password change.",
		vars["CIX_BOOTSTRAP_ADMIN_EMAIL"])
	if err != nil {
		return // cancelled, or the dialog failed and already reported
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return
	}

	// The reset runs cix-server against the database directly, so it needs the
	// runtime. In observe-only mode — where another installation owns the agent
	// and we never installed one — this is the only feature that does, so the
	// download is offered here rather than forced at startup.
	if !runtimeReady() {
		ok, err := confirm("Download the cix server?",
			"Resetting a password runs cix-server against the database directly, and it is not "+
				"installed on this Mac yet.\n\nDownloading it is about 40 MB and changes nothing "+
				"about the installation you already have.",
			"Download")
		if err != nil || !ok {
			return
		}
		if err := ensureRuntime(m.updater, m.setProgress); err != nil {
			_ = alert("Could not install the cix server", err.Error())
			return
		}
		m.setProgress("")
	}

	server, err := runtimeServerPath()
	if err != nil {
		_ = alert("cix", fmt.Sprintf("Could not locate the cix server.\n\n%v", err))
		return
	}

	password, err := runResetPassword(server, vars, email)
	if err != nil {
		_ = alert("Could not reset the password", err.Error())
		return
	}

	_ = alert("Password reset", fmt.Sprintf(
		"Account:\n%s\n\nTemporary password:\n%s\n\n"+
			"You will be asked to change it at the next sign-in. Other sessions for this "+
			"account have been signed out.", email, password))
}

// runResetPassword executes the reset and returns the generated password.
//
// The returned error is safe to display. The command's own error output is not:
// on an unknown address it lists every account in the database
// (resetpassword.go, "existing users: …"). That is defensible for an operator
// at a terminal who already holds the DB file, and indefensible in a GUI alert,
// where a typo would enumerate accounts to whoever is looking at the screen. So
// the full output goes to the log and the dialog gets one sentence.
func runResetPassword(server string, vars map[string]string, email string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, server, "-reset-password", email)

	// The subprocess reads config from the environment, so it must see the same
	// database the server uses. Without these it would fall back to the default
	// path and refuse — or worse, on an older build, act on the wrong database.
	cmd.Env = append(os.Environ(),
		"CIX_SQLITE_PATH="+vars["CIX_SQLITE_PATH"],
		"CIX_DATA_DIR="+vars["CIX_DATA_DIR"],
	)

	// Empty stdin, not the terminal's: the command reads a password from a pipe
	// when one is attached and generates one otherwise. /dev/null reads as an
	// empty first line, which it treats as "generate" — the branch we want, and
	// the only one that gives us a password to show.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()
	cmd.Stdin = devNull

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logf("reset-password for %q failed: %v; stderr: %s", email, err, strings.TrimSpace(stderr.String()))
		if ctx.Err() != nil {
			return "", fmt.Errorf("the reset command did not finish in time")
		}
		// Matched against the server's wording, and deliberately answered with
		// less than it said.
		if strings.Contains(stderr.String(), "no user with email") {
			return "", fmt.Errorf("No account with that email address.")
		}
		if strings.Contains(stderr.String(), "database not found") {
			return "", fmt.Errorf("The database could not be found. Check CIX_SQLITE_PATH in ~/.cix/server.env.")
		}
		return "", fmt.Errorf("The password could not be reset. See ~/.cix/logs/launcher.log for details.")
	}

	password, ok := parseTemporaryPassword(stdout.String())
	if !ok {
		// The reset itself succeeded — the command exited zero — so saying it
		// failed would be wrong and would invite a second, pointless attempt.
		logf("reset-password for %q succeeded but no password line was found in: %s", email, stdout.String())
		return "", fmt.Errorf("The password was reset, but cix could not read the new password from the output. " +
			"See ~/.cix/logs/launcher.log.")
	}
	logf("reset-password for %q succeeded", email)
	return password, nil
}

// parseTemporaryPassword pulls the password out of the command's stdout.
//
// The contract is one line, "Temporary password: <value>", printed only when
// the password was generated rather than piped in (resetpassword.go). Matching
// the prefix rather than a line index keeps this working when the surrounding
// lines change — the DISABLED-account warning, for one, is conditional.
func parseTemporaryPassword(stdout string) (string, bool) {
	const marker = "Temporary password: "
	for line := range strings.SplitSeq(stdout, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), marker); ok {
			if pw := strings.TrimSpace(rest); pw != "" {
				return pw, true
			}
		}
	}
	return "", false
}
