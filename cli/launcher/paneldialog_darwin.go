package main

/*
#include <stdlib.h>
void panel_set_dialog(const char *json);
void panel_open(void);
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// In-panel dialogs.
//
// Every window this app used to open through osascript — alerts, questions,
// the email prompt, the password display — renders inside the panel instead,
// as an area that takes over its content (see panel.html's #dialog). One
// window, one place to look, one visual language.
//
// The mechanics: a dialog request is pushed to the webview as JSON, the panel
// is fronted so the request is actually visible, and the calling goroutine
// blocks on a channel until the user answers, closes the panel (a dialog
// whose window went away is answered "no"), or a generous timeout expires.
// Dialogs serialise on a mutex — two questions at once is a UI bug, not a
// feature.
//
// The osascript versions in dialog_darwin.go remain as the fallback for the
// narrow window before the webview has loaded, and for anything that must be
// said when the panel cannot exist (a translocated bundle refusing to run).

// panelUIUp flips once the AppKit side has called goPanelReady; before that
// there is no webview to draw a dialog in.
var panelUIUp atomic.Bool

type panelDialogSpec struct {
	Kind          string `json:"kind"` // "alert" | "confirm" | "prompt" | "secret"
	Title         string `json:"title"`
	Message       string `json:"message,omitempty"`
	OKLabel       string `json:"okLabel,omitempty"`
	CancelLabel   string `json:"cancelLabel,omitempty"`
	DefaultAnswer string `json:"defaultAnswer,omitempty"`
	Secret        string `json:"secret,omitempty"`
	Note          string `json:"note,omitempty"`
}

type panelDialogResult struct {
	OK   bool
	Text string
}

var dialogState struct {
	mu      sync.Mutex // serialises dialogs
	pending chan panelDialogResult
	pmu     sync.Mutex // guards pending
}

// showPanelDialog runs one dialog to completion and returns the answer.
func showPanelDialog(spec panelDialogSpec) (panelDialogResult, error) {
	dialogState.mu.Lock()
	defer dialogState.mu.Unlock()

	ch := make(chan panelDialogResult, 1)
	dialogState.pmu.Lock()
	dialogState.pending = ch
	dialogState.pmu.Unlock()

	b, err := json.Marshal(spec)
	if err != nil {
		return panelDialogResult{}, err
	}
	pushDialogJSON(string(b))
	panelOpen()

	defer func() {
		dialogState.pmu.Lock()
		dialogState.pending = nil
		dialogState.pmu.Unlock()
		// Clear the dialog whichever way this ends — a timeout must not leave
		// a zombie question on screen.
		pushDialogJSON("null")
	}()

	// The same order of patience osascript got: long enough to walk away and
	// come back, bounded so an abandoned question cannot wedge a goroutine
	// holding the busy claim forever.
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(10 * time.Minute):
		return panelDialogResult{}, errors.New("the dialog was not answered")
	}
}

// showPanelBusy puts up a modal card with a loader and no buttons — the
// placeholder for a result that is on its way (an update check, most of all).
// It answers nothing and blocks nobody; the next real dialog replaces it, and
// clearPanelBusy removes it on paths that end without one. No-op before the
// panel exists.
func showPanelBusy(title string) {
	if !panelUIUp.Load() {
		return
	}
	b, err := json.Marshal(panelDialogSpec{Kind: "busy", Title: title})
	if err != nil {
		return
	}
	pushDialogJSON(string(b))
	panelOpen()
}

func clearPanelBusy() {
	if !panelUIUp.Load() {
		return
	}
	pushDialogJSON("null")
}

// resolvePanelDialog delivers an answer from the webview (or a panel-closed
// event). Safe to call when nothing is pending.
func resolvePanelDialog(r panelDialogResult) {
	dialogState.pmu.Lock()
	defer dialogState.pmu.Unlock()
	if dialogState.pending == nil {
		return
	}
	select {
	case dialogState.pending <- r:
	default:
	}
	dialogState.pending = nil
}

func pushDialogJSON(s string) {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	C.panel_set_dialog(cs)
}

// panelOpen fronts the panel so a dialog is actually seen.
func panelOpen() {
	C.panel_open()
}

// --- The user-facing dialog API. Same names and contracts as the osascript
// layer so every caller works unchanged; each routes to the panel once it is
// up and falls back to osascript before that.

func alert(title, message string) error {
	if !panelUIUp.Load() {
		return osaAlert(title, message)
	}
	_, err := showPanelDialog(panelDialogSpec{
		Kind: "alert", Title: title, Message: message,
	})
	return err
}

// alertWithSecret shows a credential and puts it on the clipboard.
//
// The copying happens when the dialog opens, not on a button, and the note
// says so — reaching for the clipboard is the next thing anyone does with a
// password the app just generated on purpose. The value itself renders in a
// selectable mono block, unlike the rest of the panel.
func alertWithSecret(title, message, secret, secretName string) error {
	note := fmt.Sprintf("The %s is on your clipboard.", secretName)
	if err := copyToClipboard(secret); err != nil {
		logf("could not copy the %s to the clipboard: %v", secretName, err)
		note = fmt.Sprintf("The %s could not be copied to your clipboard — select it above.", secretName)
	}
	if !panelUIUp.Load() {
		return osaAlert(title, message+"\n\n"+secret+"\n\n"+note)
	}
	_, err := showPanelDialog(panelDialogSpec{
		Kind: "secret", Title: title, Message: message,
		Secret: secret, Note: note,
	})
	return err
}

// prompt asks for one line of text. Returns errCancelled if the user cancels
// or closes the panel.
func prompt(title, message, defaultAnswer string) (string, error) {
	if !panelUIUp.Load() {
		return osaPrompt(title, message, defaultAnswer)
	}
	r, err := showPanelDialog(panelDialogSpec{
		Kind: "prompt", Title: title, Message: message,
		DefaultAnswer: defaultAnswer,
	})
	if err != nil {
		return "", err
	}
	if !r.OK {
		return "", errCancelled
	}
	return r.Text, nil
}

// confirm shows a two-button question. Returns false when the user declines.
func confirm(title, message, okLabel string) (bool, error) {
	return ask(title, message, okLabel, "Cancel")
}

// ask shows a two-button question with both labels spelled out. yesLabel is
// the highlighted button; dismissing the dialog counts as no.
func ask(title, message, yesLabel, noLabel string) (bool, error) {
	if !panelUIUp.Load() {
		return osaAsk(title, message, yesLabel, noLabel)
	}
	r, err := showPanelDialog(panelDialogSpec{
		Kind: "confirm", Title: title, Message: message,
		OKLabel: yesLabel, CancelLabel: noLabel,
	})
	if err != nil {
		return false, err
	}
	return r.OK, nil
}
