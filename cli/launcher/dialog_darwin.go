package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// osascript dialogs — the FALLBACK layer.
//
// The app's dialogs live inside the panel now (paneldialog_darwin.go and
// panel.html's #dialog): alert/confirm/ask/prompt/alertWithSecret there route
// to the webview once the AppKit side is up. What remains here is the same
// primitives over osascript, used only in the window before the panel exists —
// a translocated bundle refusing to run, a version query gone wrong — where a
// native modal is the only surface available.
//
// Two rules, both load-bearing:
//   - Every string that reaches AppleScript goes through quoteAS. Text here is
//     not always ours — email addresses, server errors and file paths all end up
//     in dialogs, and an unescaped quote turns a message into a syntax error at
//     best and an injected statement at worst.
//   - Every invocation is bounded. A modal that never returns would wedge the
//     launcher with no window to close, because there is no Dock icon.

// quoteAS renders a Go string as an AppleScript string literal.
func quoteAS(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	// AppleScript has no escape for a literal newline inside a quoted string;
	// concatenating `return` is the idiomatic way to express one.
	parts := strings.Split(s, "\n")
	for i, p := range parts {
		parts[i] = `"` + r.Replace(p) + `"`
	}
	return strings.Join(parts, " & return & ")
}

// dialogIcon is the POSIX path to the icon osascript dialogs are drawn with.
// Set once at startup from the bundle; empty when running outside a .app.
var dialogIcon string

// osaAlert shows a modal informational dialog and blocks until dismissed.
//
// `display dialog` rather than the more obvious `display alert`, for one
// reason: an alert is drawn with the icon of the process that ran the script,
// which here is osascript — so the app's own dialogs came up wearing a generic
// folder icon. `display dialog` takes an explicit icon.
func osaAlert(title, message string) error {
	var script string
	if dialogIcon != "" {
		script = fmt.Sprintf(
			`display dialog %s with title %s with icon POSIX file %s buttons {"OK"} default button "OK"`,
			quoteAS(message), quoteAS(title), quoteAS(dialogIcon),
		)
	} else {
		script = fmt.Sprintf(
			`display alert %s message %s as informational buttons {"OK"} default button "OK"`,
			quoteAS(title), quoteAS(message),
		)
	}
	return runOsascript(2*time.Minute, script)
}

// copyToClipboard pipes a value to pbcopy.
//
// Not AppleScript's `set the clipboard to`: that would put the secret into a
// script string, where it is one quoting mistake away from being interpreted.
// A pipe carries bytes and nothing else.
func copyToClipboard(s string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

// errCancelled is returned when the user dismissed a dialog instead of
// answering it. osascript reports this as exit status 1 — the same status as a
// real failure — so it has to be told apart from the error text.
var errCancelled = errors.New("cancelled by user")

// isUserCancelled recognises AppleScript's user-cancelled error.
//
// Match the OSStatus, not the sentence. The message is localised: on this
// project's own development Mac, running a British locale, osascript says "User
// cancelled" with two Ls, while the American spelling has one — so a string
// match on either is wrong somewhere, and was. -128 (userCanceledErr) is a
// number and says the same thing in every language.
func isUserCancelled(stderr string) bool {
	return strings.Contains(stderr, "(-128)")
}

// osaPrompt asks for one line of text. Returns errCancelled on cancel.
func osaPrompt(title, message, defaultAnswer string) (string, error) {
	script := fmt.Sprintf(
		`display dialog %s with title %s default answer %s %s buttons {"Cancel", "OK"} default button "OK" cancel button "Cancel"`,
		quoteAS(message), quoteAS(title), quoteAS(defaultAnswer), iconClause(),
	)
	out, err := outputOsascript(5*time.Minute, script)
	if err != nil {
		return "", err
	}
	// osascript prints `button returned:OK, text returned:<value>`. The text is
	// last, and a value containing ", text returned:" is not reachable because
	// the field is single-line — so cutting on the marker is safe.
	_, answer, ok := strings.Cut(out, "text returned:")
	if !ok {
		return "", fmt.Errorf("unexpected osascript output: %q", out)
	}
	return strings.TrimSpace(answer), nil
}

// osaAsk shows a two-button question with both labels spelled out.
// yesLabel is the default button. Dismissing the dialog counts as no.
func osaAsk(title, message, yesLabel, noLabel string) (bool, error) {
	script := fmt.Sprintf(
		`display dialog %s with title %s %s buttons {%s, %s} default button %s cancel button %s`,
		quoteAS(message), quoteAS(title), iconClause(),
		quoteAS(noLabel), quoteAS(yesLabel), quoteAS(yesLabel), quoteAS(noLabel),
	)
	if _, err := outputOsascript(5*time.Minute, script); err != nil {
		if errors.Is(err, errCancelled) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func iconClause() string {
	if dialogIcon == "" {
		return ""
	}
	return "with icon POSIX file " + quoteAS(dialogIcon)
}

func outputOsascript(timeout time.Duration, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("osascript timed out after %s", timeout)
		}
		if isUserCancelled(stderr.String()) {
			return "", errCancelled
		}
		return "", fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func runOsascript(timeout time.Duration, script string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("osascript timed out after %s", timeout)
		}
		return fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
