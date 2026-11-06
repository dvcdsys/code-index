package main

/*
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
void panel_set_dialog(const char *json);
void panel_open(void);

void panel_run(const char *iconPath, const char *html);
void panel_set_state(const char *json);
void panel_set_title(const char *title);
void panel_quit(void);
*/
import "C"

import (
	"encoding/json"
	"runtime"
	"unsafe"
)

// AppKit is main-thread-only, and [NSApp run] must be started from the thread
// the process was born on. Locking in init — before main() runs — is the
// documented way to guarantee the main goroutine still owns that thread by the
// time panelRun is called.
func init() {
	runtime.LockOSThread()
}

// The cgo bridge to panel_darwin.m. Three exported callbacks come back from
// the Objective-C side; all of them arrive on the AppKit main thread, so each
// hands off to Go-land immediately and returns.

//export goPanelReady
func goPanelReady() {
	// From here on, dialogs render inside the panel instead of via osascript —
	// the webview may still be loading, but requests pushed before it finishes
	// are replayed on load (see pendingDialog in panel_darwin.m).
	panelUIUp.Store(true)
	if panelHooks.onReady != nil {
		// Off the main thread: onReady starts pollers and may do I/O.
		go panelHooks.onReady()
	}
}

//export goPanelExit
func goPanelExit() {
	if panelHooks.onExit != nil {
		panelHooks.onExit()
	}
}

//export goPanelAction
func goPanelAction(cjson *C.char) {
	raw := C.GoString(cjson)
	var a panelAction
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		logf("panel sent unparseable action %q: %v", raw, err)
		return
	}
	if panelHooks.onAction != nil {
		// Every action handler blocks (dialogs, restarts), and this callback
		// is on the AppKit main thread.
		go panelHooks.onAction(a)
	}
}

// panelRun starts the AppKit application and never returns until quit.
// Must be called on the main goroutine — AppKit demands the main thread, and
// main_darwin.go's runtime.LockOSThread guarantee comes from Go putting main()
// there.
func panelRun(iconPath string) {
	cIcon := C.CString(iconPath)
	cHTML := C.CString(panelHTML)
	defer C.free(unsafe.Pointer(cIcon))
	defer C.free(unsafe.Pointer(cHTML))
	C.panel_run(cIcon, cHTML)
}

// panelSetState pushes the render-state JSON to the webview. Safe from any
// goroutine.
func panelSetState(state any) {
	b, err := json.Marshal(state)
	if err != nil {
		logf("could not marshal panel state: %v", err)
		return
	}
	cs := C.CString(string(b))
	defer C.free(unsafe.Pointer(cs))
	C.panel_set_state(cs)
}

// panelSetTitle puts text beside the menu bar icon (progress messages), or
// clears it with "".
func panelSetTitle(title string) {
	cs := C.CString(title)
	defer C.free(unsafe.Pointer(cs))
	C.panel_set_title(cs)
}

// panelQuit terminates the application; goPanelExit fires on the way out.
func panelQuit() {
	C.panel_quit()
}

// pushDialogJSON hands a dialog request (or "null" to clear) to the webview.
func pushDialogJSON(s string) {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	C.panel_set_dialog(cs)
}

// panelOpen fronts the panel so a dialog is actually seen.
func panelOpen() {
	C.panel_open()
}
