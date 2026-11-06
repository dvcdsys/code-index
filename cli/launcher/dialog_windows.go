package main

import (
	"errors"
	"unicode/utf16"
	"unsafe"
)

// Native dialogs — the FALLBACK layer, as on macOS. Everything the app says
// normally renders inside the panel; these are for the window before the panel
// exists, and for the case where it cannot exist at all (no WebView2 Runtime).

func messageBox(title, message string, flags uintptr) uintptr {
	r, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(utf16Ptr(message))),
		uintptr(unsafe.Pointer(utf16Ptr(title))), flags|mbSetForeground|mbTopmost)
	return r
}

func utf16Ptr(s string) *uint16 { return &append(utf16.Encode([]rune(s)), 0)[0] }

func nativeAlert(title, message string) error {
	messageBox(title, message, mbOK|mbIconInfo)
	return nil
}

// nativeAsk shows Yes/No. A message box cannot carry custom button labels, so
// the labels are folded into the text; yes is always the affirmative.
func nativeAsk(title, message, yes, no string) (bool, error) {
	text := message + "\n\nYes: " + yes + "\nNo: " + no
	return messageBox(title, text, mbYesNo|mbIconQuestion) == idYes, nil
}

// nativePrompt cannot be done with a message box, and nothing in the app asks
// for text before the panel is up. Fail loudly rather than invent an answer.
func nativePrompt(title, message, defaultAnswer string) (string, error) {
	return "", errors.New("a text prompt needs the cix panel, which is not available (is the WebView2 Runtime installed?)")
}

// copyToClipboard puts text on the clipboard as CF_UNICODETEXT.
func copyToClipboard(s string) error {
	u := append(utf16.Encode([]rune(s)), 0)
	if r, _, err := procOpenClipboard.Call(0); r == 0 {
		return err
	}
	defer procCloseClipboard.Call()
	_, _, _ = procEmptyClipboard.Call()

	size := uintptr(len(u) * 2)
	h, _, err := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return err
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return errors.New("could not lock clipboard memory")
	}
	copy(unsafe.Slice((*uint16)(*(*unsafe.Pointer)(unsafe.Pointer(&p))), len(u)), u)
	_, _, _ = procGlobalUnlock.Call(h)
	if r, _, err := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		return err
	}
	return nil
}
