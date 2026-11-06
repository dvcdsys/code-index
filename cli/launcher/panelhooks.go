//go:build darwin || windows

package main

import _ "embed"

// The shell-independent half of the panel bridge: the HTML, the hook table the
// menu layer fills in, and the shape of one user gesture. The native shell —
// panel_darwin.go + panel_darwin.m on macOS, shell_windows.go on Windows —
// owns the window and calls back through these.

//go:embed panel.html
var panelHTML string

// panelHooks is set once, before panelRun, by the menu layer. Not guarded by a
// lock: writes happen strictly before the shell starts delivering callbacks.
var panelHooks struct {
	onReady  func()
	onExit   func()
	onAction func(action panelAction)
}

// panelAction is one user gesture inside the panel, as posted by panel.html.
// OK and Text carry a dialog's answer and are meaningless for other actions.
type panelAction struct {
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	Text   string `json:"text"`
}
