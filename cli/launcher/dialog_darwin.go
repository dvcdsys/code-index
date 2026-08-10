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

// alert shows a modal informational alert and blocks until it is dismissed.
func alert(title, message string) error {
	script := fmt.Sprintf(
		`display alert %s message %s as informational buttons {"OK"} default button "OK"`,
		quoteAS(title), quoteAS(message),
	)
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
