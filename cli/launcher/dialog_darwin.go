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

// osascript is the dialog mechanism for the whole launcher.
//
// The menu-bar library (Phase 2) has no dialog API, and pulling in a second GUI
// toolkit to draw three alerts would double the bundle for no gain. AppleScript
// alerts are native, need no linkage, and survive the app being LSUIElement.
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

// dialogIcon is the POSIX path to the icon dialogs are drawn with. Set once at
// startup from the bundle; empty when the launcher runs outside a .app.
var dialogIcon string

// alert shows a modal informational dialog and blocks until it is dismissed.
//
// `display dialog` rather than the more obvious `display alert`, for one
// reason: an alert is drawn with the icon of the process that ran the script,
// which here is osascript — so the app's own dialogs came up wearing a generic
// folder icon. `display dialog` takes an explicit icon. The cost is that the
// title is a window title instead of bold body text; the icon is worth more.
// It is also the primitive Phase 2 needs anyway, since only `display dialog`
// supports `default answer` for text input.
func alert(title, message string) error {
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

// alertWithCopy is alert() plus a button that puts secret on the clipboard.
//
// AppleScript cannot make a run of text clickable — `display dialog` draws one
// static string — so the click target has to be a button. That is the whole
// reason this is not simply "click the password".
//
// It re-shows the dialog after copying rather than dismissing. A password
// displayed once is exactly the thing someone reaches for a second time, and a
// window that disappears on the first attempt is how people end up reading
// credentials off a screenshot. The loop always terminates: every turn of it
// waits for a click.
func alertWithCopy(title, message, secret, copyLabel string) error {
	body := message
	for {
		script := fmt.Sprintf(
			`display dialog %s with title %s %s buttons {%s, "Done"} default button "Done"`,
			quoteAS(body), quoteAS(title), iconClause(), quoteAS(copyLabel),
		)
		out, err := outputOsascript(5*time.Minute, script)
		if err != nil {
			// Dismissing the window is a perfectly good way to say "I have it".
			if errors.Is(err, errCancelled) {
				return nil
			}
			return err
		}
		if !strings.Contains(out, copyLabel) {
			return nil
		}

		if err := copyToClipboard(secret); err != nil {
			logf("could not copy to the clipboard: %v", err)
			body = message + "\n\nCould not copy to the clipboard."
			continue
		}
		body = message + "\n\nCopied to the clipboard."
	}
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

// prompt asks for one line of text. Returns errCancelled if the user cancels.
func prompt(title, message, defaultAnswer string) (string, error) {
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

// confirm shows a two-button question. Returns false when the user declines.
func confirm(title, message, okLabel string) (bool, error) {
	return ask(title, message, okLabel, "Cancel")
}

// ask shows a two-button question with both labels spelled out.
//
// Separate from confirm because not every choice has a "cancel" side. "Take
// Over" versus "Leave It Alone" are two real options, and labelling the second
// one Cancel would imply it does nothing — when in fact it decides how the app
// behaves from then on.
//
// yesLabel is the default button. Dismissing the dialog counts as no.
func ask(title, message, yesLabel, noLabel string) (bool, error) {
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
