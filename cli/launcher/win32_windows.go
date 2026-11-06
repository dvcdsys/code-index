package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A deliberately thin layer over the Win32 calls the shell needs. Only what
// shell_windows.go uses is declared — this is not a general binding, and keeping
// it small keeps the dependency list at x/sys/windows plus the WebView2 wrapper.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW          = user32.NewProc("RegisterClassExW")
	procCreateWindowExW           = user32.NewProc("CreateWindowExW")
	procDefWindowProcW            = user32.NewProc("DefWindowProcW")
	procDestroyWindow             = user32.NewProc("DestroyWindow")
	procGetMessageW               = user32.NewProc("GetMessageW")
	procTranslateMessage          = user32.NewProc("TranslateMessage")
	procDispatchMessageW          = user32.NewProc("DispatchMessageW")
	procPostMessageW              = user32.NewProc("PostMessageW")
	procSendMessageW              = user32.NewProc("SendMessageW")
	procPostQuitMessage           = user32.NewProc("PostQuitMessage")
	procShowWindow                = user32.NewProc("ShowWindow")
	procSetWindowPos              = user32.NewProc("SetWindowPos")
	procGetWindowRect             = user32.NewProc("GetWindowRect")
	procGetCursorPos              = user32.NewProc("GetCursorPos")
	procSetForegroundWindow       = user32.NewProc("SetForegroundWindow")
	procIsWindowVisible           = user32.NewProc("IsWindowVisible")
	procLoadCursorW               = user32.NewProc("LoadCursorW")
	procLoadIconW                 = user32.NewProc("LoadIconW")
	procCreateIconIndirect        = user32.NewProc("CreateIconIndirect")
	procDestroyIcon               = user32.NewProc("DestroyIcon")
	procMonitorFromPoint          = user32.NewProc("MonitorFromPoint")
	procMonitorFromRect           = user32.NewProc("MonitorFromRect")
	procGetMonitorInfoW           = user32.NewProc("GetMonitorInfoW")
	procGetDpiForWindow           = user32.NewProc("GetDpiForWindow")
	procGetDpiForSystem           = user32.NewProc("GetDpiForSystem")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procRegisterWindowMessageW    = user32.NewProc("RegisterWindowMessageW")
	procFindWindowW               = user32.NewProc("FindWindowW")
	procGetClientRect             = user32.NewProc("GetClientRect")
	procCreatePopupMenu           = user32.NewProc("CreatePopupMenu")
	procAppendMenuW               = user32.NewProc("AppendMenuW")
	procTrackPopupMenu            = user32.NewProc("TrackPopupMenu")
	procDestroyMenu               = user32.NewProc("DestroyMenu")
	procGetSystemMetrics          = user32.NewProc("GetSystemMetrics")
	procMessageBoxW               = user32.NewProc("MessageBoxW")
	procOpenClipboard             = user32.NewProc("OpenClipboard")
	procCloseClipboard            = user32.NewProc("CloseClipboard")
	procEmptyClipboard            = user32.NewProc("EmptyClipboard")
	procSetClipboardData          = user32.NewProc("SetClipboardData")

	procShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
	procShellNotifyIconGetRect = shell32.NewProc("Shell_NotifyIconGetRect")
	procShellExecuteW          = shell32.NewProc("ShellExecuteW")

	procCreateDIBSection = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap     = gdi32.NewProc("CreateBitmap")
	procDeleteObject     = gdi32.NewProc("DeleteObject")

	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")

	procGlobalAlloc  = kernel32.NewProc("GlobalAlloc")
	procGlobalLock   = kernel32.NewProc("GlobalLock")
	procGlobalUnlock = kernel32.NewProc("GlobalUnlock")
)

const (
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmActivate    = 0x0006
	wmCommand     = 0x0111
	wmTimer       = 0x0113
	wmSettingChg  = 0x001A
	wmDpiChanged  = 0x02E0
	wmContextMenu = 0x007B
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmApp         = 0x8000

	waInactive = 0

	wsPopup         = 0x80000000
	wsExToolWindow  = 0x00000080
	wsExTopmost     = 0x00000008
	csDropShadow    = 0x00020000
	swHide          = 0
	swShow          = 5
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpShowWindow   = 0x0040
	hwndTopmost     = ^uintptr(0) // (HWND)-1
	idcArrow        = 32512
	idiApplication  = 32512
	smCxSmIcon      = 49
	smCySmIcon      = 50
	monitorNearest  = 2
	tpmRightAlign   = 0x0008
	tpmBottomAlign  = 0x0020
	tpmReturnCmd    = 0x0100
	tpmRightButton  = 0x0002
	mfString        = 0x0000
	mfSeparator     = 0x0800
	cfUnicodeText   = 13
	gmemMoveable    = 0x0002
	mbOK            = 0x00000000
	mbYesNo         = 0x00000004
	mbIconInfo      = 0x00000040
	mbIconQuestion  = 0x00000020
	mbSetForeground = 0x00010000
	mbTopmost       = 0x00040000
	idYes           = 6

	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nimSetVersion = 4
	nifMessage    = 0x01
	nifIcon       = 0x02
	nifTip        = 0x04
	nifShowTip    = 0x80
	notifyVersion = 4
	ninSelect     = 0x400 // WM_USER + 0 under NOTIFYICON_VERSION_4
	ninKeySelect  = 0x401

	dwmwaUseImmersiveDarkMode = 20
	dwmwaWindowCornerPref     = 33
	dwmwaBorderColor          = 34
	dwmwcpRound               = 2
)

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type msg struct {
	Hwnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// notifyIconData is NOTIFYICONDATAW at its current (Vista+) size.
type notifyIconData struct {
	Size            uint32
	Hwnd            windows.HWND
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32 // union with uTimeout
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GuidItem        guid
	BalloonIcon     windows.Handle
}

type notifyIconIdentifier struct {
	Size uint32
	Hwnd windows.HWND
	ID   uint32
	Guid guid
}

type iconInfo struct {
	IsIcon   int32
	XHotspot uint32
	YHotspot uint32
	Mask     windows.Handle
	Color    windows.Handle
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

func wstr(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func loword(v uintptr) uint32 { return uint32(v & 0xFFFF) }
func hiword(v uintptr) uint32 { return uint32((v >> 16) & 0xFFFF) }

func createWindow(exStyle uint32, class string, style uint32, x, y, w, h int32, parent windows.HWND) (windows.HWND, error) {
	hInst := moduleHandle()
	r, _, err := procCreateWindowExW.Call(
		uintptr(exStyle), uintptr(unsafe.Pointer(wstr(class))), uintptr(unsafe.Pointer(wstr("cix"))),
		uintptr(style), uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		uintptr(parent), 0, uintptr(hInst), 0)
	if r == 0 {
		return 0, err
	}
	return windows.HWND(r), nil
}

func registerClass(name string, proc uintptr, style uint32) error {
	hInst := moduleHandle()
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	wc := wndClassEx{
		Style:     style,
		WndProc:   proc,
		Instance:  hInst,
		Cursor:    windows.Handle(cursor),
		ClassName: wstr(name),
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return err
	}
	return nil
}

func newWndProc(fn func(hwnd windows.HWND, m uint32, wp, lp uintptr) uintptr) uintptr {
	return syscall.NewCallback(func(hwnd, m, wp, lp uintptr) uintptr {
		return fn(windows.HWND(hwnd), uint32(m), wp, lp)
	})
}

func defWindowProc(hwnd windows.HWND, m uint32, wp, lp uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(m), wp, lp)
	return r
}

func postMessage(hwnd windows.HWND, m uint32, wp, lp uintptr) {
	_, _, _ = procPostMessageW.Call(uintptr(hwnd), uintptr(m), wp, lp)
}

func dwmSetInt(hwnd windows.HWND, attr uint32, v int32) {
	_, _, _ = procDwmSetWindowAttribute.Call(uintptr(hwnd), uintptr(attr), uintptr(unsafe.Pointer(&v)), 4)
}

func dpiForWindow(hwnd windows.HWND) int {
	if r, _, _ := procGetDpiForWindow.Call(uintptr(hwnd)); r != 0 {
		return int(r)
	}
	if r, _, _ := procGetDpiForSystem.Call(); r != 0 {
		return int(r)
	}
	return 96
}

func moduleHandle() windows.Handle {
	var h windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &h)
	return h
}
