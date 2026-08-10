package main

import (
	"context"
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
