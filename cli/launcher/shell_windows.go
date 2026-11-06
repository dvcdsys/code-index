package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The Windows shell: a tray icon, and a borderless popup that hosts panel.html
// in WebView2. It is the counterpart of panel_darwin.m — a projector. Every
// decision lives in menu.go; this file only draws what Go pushes (panelSetState)
// and reports what the user does (panelHooks.onAction).
//
// Threading: Win32 windows and the WebView2 COM objects belong to the thread
// that created them, so the main goroutine is locked to its OS thread in init
// and runs the message loop. Anything another goroutine wants done to the UI is
// queued with uiPost and executed on that thread.

func init() { runtime.LockOSThread() }

const (
	mainClass  = "CixTrayWnd"
	panelClass = "CixPanelWnd"

	wmRunFunc   = wmApp + 1 // drain the uiPost queue
	wmTray      = wmApp + 2 // tray icon callback
	wmOpenPanel = wmApp + 3 // sent by a second launch of the app
	wmCopyData  = 0x004A

	trayIconID = 1

	cmdOpen      = 1
	cmdDashboard = 2
	cmdQuit      = 3

	// The design's width, in CSS pixels; height follows the content (the page
	// reports it after every render).
	panelCSSWidth = 392
	panelMargin   = 8 // CSS px between the panel and the screen edge / taskbar
)

//go:embed tray_glyph.png
var trayGlyph []byte

var sh struct {
	hwndMain  windows.HWND
	hwndPanel windows.HWND
	chromium  *edge.Chromium

	mu    sync.Mutex
	queue []func()

	webReady  bool // the page finished its first navigation
	pendState string
	pendDlg   string

	icon         windows.Handle
	tip          string
	cssHeight    int
	visible      bool
	anchorBottom bool
	lastHide     time.Time
	taskbarMsg   uint32

	light bool // the taskbar / apps theme at last check, for the tray glyph
}

// uiPost runs fn on the UI thread. Safe from any goroutine.
func uiPost(fn func()) {
	sh.mu.Lock()
	sh.queue = append(sh.queue, fn)
	hwnd := sh.hwndMain
	sh.mu.Unlock()
	if hwnd != 0 {
		postMessage(hwnd, wmRunFunc, 0, 0)
	}
}

func drainQueue() {
	sh.mu.Lock()
	q := sh.queue
	sh.queue = nil
	sh.mu.Unlock()
	for _, fn := range q {
		fn()
	}
}

// panelRun builds the tray icon and the panel, then runs the message loop until
// quit. Must be called from the main goroutine.
func panelRun(_ string) {
	// Per-monitor v2: without it Windows bitmap-scales the whole panel on a
	// high-DPI screen and the text blurs.
	_, _, _ = procSetProcessDpiAwarenessCtx.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	_ = windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)

	sh.cssHeight = 220
	sh.taskbarMsg = registerMessage("TaskbarCreated")

	mainProc := newWndProc(mainWndProc)
	panelProc := newWndProc(panelWndProc)
	if err := registerClass(mainClass, mainProc, 0); err != nil {
		logf("could not register the tray window class: %v", err)
		return
	}
	if err := registerClass(panelClass, panelProc, csDropShadow); err != nil {
		logf("could not register the panel window class: %v", err)
		return
	}

	var err error
	// A hidden top-level window rather than a message-only one: broadcast
	// messages (TaskbarCreated, theme changes) are not delivered to those.
	sh.hwndMain, err = createWindow(wsExToolWindow, mainClass, wsPopup, 0, 0, 0, 0, 0)
	if err != nil {
		logf("could not create the tray window: %v", err)
		return
	}
	sh.hwndPanel, err = createWindow(wsExToolWindow|wsExTopmost, panelClass, wsPopup, 0, 0, panelCSSWidth, 200, 0)
	if err != nil {
		logf("could not create the panel window: %v", err)
		return
	}
	dwmSetInt(sh.hwndPanel, dwmwaWindowCornerPref, dwmwcpRound)
	applyPanelTheme()

	sh.light = systemUsesLightTheme()
	addTrayIcon()

	if initWebView() {
		panelUIUp.Store(true)
	} else {
		logf("the panel is unavailable; the tray menu still works")
	}
	if panelHooks.onReady != nil {
		go panelHooks.onReady()
	}

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	removeTrayIcon()
	if panelHooks.onExit != nil {
		panelHooks.onExit()
	}
}

func registerMessage(name string) uint32 {
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(wstr(name))))
	return uint32(r)
}

// --- WebView2 ----------------------------------------------------------------

// webview2Installed reports whether the WebView2 Runtime is present. The
// wrapper library calls log.Fatalf when it cannot create its environment, which
// would take the whole app (and the tray icon the user relies on) down with it,
// so a missing runtime is detected up front instead.
func webview2Installed() bool {
	const clientGUID = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	for _, k := range []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + clientGUID},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + clientGUID},
		{registry.CURRENT_USER, `Software\Microsoft\EdgeUpdate\Clients\` + clientGUID},
	} {
		key, err := registry.OpenKey(k.root, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := key.GetStringValue("pv")
		key.Close()
		if err == nil && v != "" && v != "0.0.0.0" {
			return true
		}
	}
	return false
}

func initWebView() bool {
	if !webview2Installed() {
		logf("the WebView2 Runtime is not installed")
		return false
	}

	c := edge.NewChromium()
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		c.DataPath = filepath.Join(base, "cix", "webview2")
	}
	c.MessageCallback = onWebMessage
	c.NavigationCompletedCallback = func(_ *edge.ICoreWebView2, _ *edge.ICoreWebView2NavigationCompletedEventArgs) {
		sh.webReady = true
		pushState()
		pushDialog()
	}
	if !c.Embed(uintptr(sh.hwndPanel)) {
		logf("WebView2 could not be embedded")
		return false
	}
	sh.chromium = c

	// Opaque background in the theme's surface colour: the panel is a rounded
	// DWM window, not a layered one, so there is nothing behind the page to be
	// transparent onto.
	if c2 := c.GetController().GetICoreWebView2Controller2(); c2 != nil {
		bg := edge.COREWEBVIEW2_COLOR{A: 255, R: 0xF7, G: 0xEE, B: 0xDC}
		if !sh.light {
			bg = edge.COREWEBVIEW2_COLOR{A: 255, R: 0x17, G: 0x14, B: 0x12}
		}
		_ = c2.PutDefaultBackgroundColor(bg)
	}
	if s, err := c.GetSettings(); err == nil && s != nil {
		_ = s.PutIsStatusBarEnabled(false)
		_ = s.PutAreDefaultContextMenusEnabled(false)
		_ = s.PutAreDevToolsEnabled(false)
		_ = s.PutIsZoomControlEnabled(false)
	}
	c.Init("window.cixPlatform = 'windows';")
	c.Resize()
	c.NavigateToString(panelHTML)
	return true
}

// onWebMessage receives one JSON message posted by panel.html. It runs on the UI
// thread.
func onWebMessage(raw string) {
	var generic map[string]any
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		logf("panel sent unparseable message %q: %v", raw, err)
		return
	}
	action, _ := generic["action"].(string)

	// Layout messages are about this window, not about the server.
	switch action {
	case "height":
		if v, ok := generic["value"].(float64); ok && v >= 40 && v <= 1200 {
			setPanelHeight(int(v + 0.5))
		}
		return
	case "close":
		closePanel()
		return
	}

	var a panelAction
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return
	}
	// Button-shaped actions dismiss the panel like a menu item would; toggle rows
	// keep it open. The distinction lives in the HTML (dismiss:true).
	if d, _ := generic["dismiss"].(bool); d {
		closePanel()
	}
	if panelHooks.onAction != nil {
		go panelHooks.onAction(a)
	}
}

func evalJS(js string) {
	if sh.chromium != nil && sh.webReady {
		sh.chromium.Eval(js)
	}
}

func pushState() {
	if sh.pendState != "" {
		evalJS("window.cixRender && window.cixRender(" + sh.pendState + ")")
	}
}

func pushDialog() {
	if sh.pendDlg != "" {
		evalJS("window.cixDialog && window.cixDialog(" + sh.pendDlg + ")")
	}
}

// --- the API menu.go and paneldialog.go call ---------------------------------

// panelSetState pushes the render-state JSON to the webview.
func panelSetState(state any) {
	b, err := json.Marshal(state)
	if err != nil {
		logf("could not marshal panel state: %v", err)
		return
	}
	s := string(b)
	uiPost(func() { sh.pendState = s; pushState() })
}

// pushDialogJSON hands a dialog request (or "null" to clear) to the webview.
func pushDialogJSON(s string) {
	uiPost(func() { sh.pendDlg = s; pushDialog() })
}

// panelOpen fronts the panel so a dialog is actually seen.
func panelOpen() { uiPost(openPanel) }

// panelSetTitle shows progress text. Windows has no room beside a tray icon, so
// it goes into the tooltip.
func panelSetTitle(title string) {
	uiPost(func() {
		sh.tip = title
		modifyTrayIcon()
	})
}

// panelQuit terminates the app; the message loop ends and onExit fires.
func panelQuit() {
	uiPost(func() { _, _, _ = procDestroyWindow.Call(uintptr(sh.hwndMain)) })
}

// --- the panel window --------------------------------------------------------

func panelWndProc(hwnd windows.HWND, m uint32, wp, lp uintptr) uintptr {
	switch m {
	case wmActivate:
		if loword(wp) == waInactive && sh.visible {
			// Another window took the foreground — the equivalent of clicking
			// away from a menu.
			closePanel()
		}
		return 0
	case wmClose:
		closePanel()
		return 0
	case wmDpiChanged:
		if sh.chromium != nil {
			sh.chromium.Resize()
		}
	}
	return defWindowProc(hwnd, m, wp, lp)
}

func isVisible(hwnd windows.HWND) bool {
	r, _, _ := procIsWindowVisible.Call(uintptr(hwnd))
	return r != 0
}

func togglePanel() {
	if sh.visible {
		closePanel()
		return
	}
	// The click that hides the panel (it deactivates) and the click that would
	// reopen it are the same click on the tray icon. Without this the panel
	// flickers shut and open again.
	if time.Since(sh.lastHide) < 300*time.Millisecond {
		return
	}
	openPanel()
}

func openPanel() {
	if sh.visible || sh.hwndPanel == 0 {
		return
	}
	anchor, haveAnchor := trayRect()
	if !haveAnchor {
		var p point
		_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
		anchor = rect{p.X - 8, p.Y - 8, p.X + 8, p.Y + 8}
	}

	mon, _, _ := procMonitorFromRect.Call(uintptr(unsafe.Pointer(&anchor)), monitorNearest)
	mi := monitorInfo{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	_, _, _ = procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	dpi := monitorDPI(mon)

	w := int32(panelCSSWidth * dpi / 96)
	h := int32(sh.cssHeight * dpi / 96)
	margin := int32(panelMargin * dpi / 96)
	wa := mi.Work

	// The panel opens on the side of the icon facing the screen's interior, which
	// is whichever edge the taskbar is docked to.
	var x, y int32
	cx := (anchor.Left + anchor.Right) / 2
	cy := (anchor.Top + anchor.Bottom) / 2
	switch {
	case anchor.Top >= wa.Bottom-4: // taskbar at the bottom
		x, y, sh.anchorBottom = cx-w/2, wa.Bottom-h-margin, true
	case anchor.Bottom <= wa.Top+4: // top
		x, y, sh.anchorBottom = cx-w/2, wa.Top+margin, false
	case anchor.Left >= wa.Right-4: // right
		x, y, sh.anchorBottom = wa.Right-w-margin, cy-h/2, false
	case anchor.Right <= wa.Left+4: // left
		x, y, sh.anchorBottom = wa.Left+margin, cy-h/2, false
	default:
		x, y, sh.anchorBottom = cx-w/2, wa.Bottom-h-margin, true
	}
	x = clamp(x, wa.Left+margin, wa.Right-w-margin)
	y = clamp(y, wa.Top+margin, wa.Bottom-h-margin)

	logf("panel opens at %d,%d %dx%d (dpi %d, work %v, anchor %v found=%v)", x, y, w, h, dpi, wa, anchor, haveAnchor)
	_, _, _ = procSetWindowPos.Call(uintptr(sh.hwndPanel), hwndTopmost,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h), swpShowWindow)
	sh.visible = true
	_, _, _ = procSetForegroundWindow.Call(uintptr(sh.hwndPanel))
	if sh.chromium != nil {
		// The controller was created while the host window was hidden, and
		// WebView2 then keeps rendering off until told otherwise.
		_ = sh.chromium.Show()
		sh.chromium.Resize()
		sh.chromium.Focus()
	}
	if panelHooks.onAction != nil {
		go panelHooks.onAction(panelAction{Action: "opened"})
	}
}

func closePanel() {
	if !sh.visible {
		return
	}
	sh.visible = false
	sh.lastHide = time.Now()
	_, _, _ = procShowWindow.Call(uintptr(sh.hwndPanel), swHide)
	if sh.chromium != nil {
		_ = sh.chromium.Hide()
	}
	// Go must hear about this: an in-panel dialog whose panel disappears is a
	// dialog nobody can answer, and the Go side is blocked waiting on it.
	if panelHooks.onAction != nil {
		go panelHooks.onAction(panelAction{Action: "panel-closed"})
	}
}

// setPanelHeight resizes to the content height the page reported, keeping the
// edge nearest the taskbar fixed so the panel grows away from it.
func setPanelHeight(css int) {
	sh.cssHeight = css
	if sh.hwndPanel == 0 {
		return
	}
	dpi := dpiForWindow(sh.hwndPanel)
	var r rect
	_, _, _ = procGetWindowRect.Call(uintptr(sh.hwndPanel), uintptr(unsafe.Pointer(&r)))
	h := int32(css * dpi / 96)
	w := int32(panelCSSWidth * dpi / 96)
	y := r.Top
	if sh.anchorBottom {
		y = r.Bottom - h
	}
	_, _, _ = procSetWindowPos.Call(uintptr(sh.hwndPanel), 0,
		uintptr(r.Left), uintptr(y), uintptr(w), uintptr(h), swpNoZOrder|swpNoActivate)
	if sh.chromium != nil {
		sh.chromium.Resize()
	}
}

func clamp(v, lo, hi int32) int32 {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

var (
	shcore               = windows.NewLazySystemDLL("shcore.dll")
	procGetDpiForMonitor = shcore.NewProc("GetDpiForMonitor")
)

func monitorDPI(mon uintptr) int {
	var x, y uint32
	if r, _, _ := procGetDpiForMonitor.Call(mon, 0, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y))); r == 0 && x != 0 {
		return int(x)
	}
	return 96
}

// applyPanelTheme matches the window frame to the system theme. The border
// colour is what DWM draws around the rounded window; panel.html drops its own
// border on Windows so the two do not fight.
func applyPanelTheme() {
	light := appsUseLightTheme()
	if light {
		dwmSetInt(sh.hwndPanel, dwmwaUseImmersiveDarkMode, 0)
		dwmSetInt(sh.hwndPanel, dwmwaBorderColor, 0x000F141F) // #1F140F
	} else {
		dwmSetInt(sh.hwndPanel, dwmwaUseImmersiveDarkMode, 1)
		dwmSetInt(sh.hwndPanel, dwmwaBorderColor, 0x001F2933) // #33291F
	}
}

func personalizeBool(name string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	if err != nil {
		return true
	}
	return v != 0
}

func appsUseLightTheme() bool    { return personalizeBool("AppsUseLightTheme") }
func systemUsesLightTheme() bool { return personalizeBool("SystemUsesLightTheme") }

// --- the tray icon -----------------------------------------------------------

func mainWndProc(hwnd windows.HWND, m uint32, wp, lp uintptr) uintptr {
	switch {
	case m == wmRunFunc:
		drainQueue()
		return 0
	case m == wmOpenPanel:
		openPanel()
		return 0
	case m == wmCopyData:
		return handleCopyData(lp)
	case m == wmTray:
		switch loword(lp) {
		case ninSelect, ninKeySelect, wmLButtonUp:
			togglePanel()
		case wmContextMenu, wmRButtonUp:
			showContextMenu()
		}
		return 0
	case m == sh.taskbarMsg && sh.taskbarMsg != 0:
		// Explorer restarted and took every tray icon with it.
		addTrayIcon()
		return 0
	case m == wmSettingChg:
		if lp != 0 && windows.UTF16PtrToString((*uint16)(*(*unsafe.Pointer)(unsafe.Pointer(&lp)))) == "ImmersiveColorSet" {
			sh.light = systemUsesLightTheme()
			applyPanelTheme()
			modifyTrayIcon()
		}
		return 0
	case m == wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	return defWindowProc(hwnd, m, wp, lp)
}

func trayData() notifyIconData {
	nid := notifyIconData{Hwnd: sh.hwndMain, ID: trayIconID}
	nid.Size = uint32(unsafe.Sizeof(nid))
	return nid
}

func setTip(nid *notifyIconData) {
	tip := "cix"
	if sh.tip != "" {
		tip = "cix — " + sh.tip
	}
	copy(nid.Tip[:127], windows.StringToUTF16(tip))
}

func addTrayIcon() {
	if sh.icon != 0 {
		_, _, _ = procDestroyIcon.Call(uintptr(sh.icon))
	}
	sh.icon = buildTrayIcon(sh.light)

	nid := trayData()
	nid.Flags = nifMessage | nifIcon | nifTip | nifShowTip
	nid.CallbackMessage = wmTray
	nid.Icon = sh.icon
	setTip(&nid)
	_, _, _ = procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	nid.Version = notifyVersion
	_, _, _ = procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&nid)))
}

func modifyTrayIcon() {
	old := sh.icon
	sh.icon = buildTrayIcon(sh.light)
	nid := trayData()
	nid.Flags = nifIcon | nifTip
	nid.Icon = sh.icon
	setTip(&nid)
	_, _, _ = procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
	if old != 0 {
		_, _, _ = procDestroyIcon.Call(uintptr(old))
	}
}

func removeTrayIcon() {
	nid := trayData()
	_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	if sh.icon != 0 {
		_, _, _ = procDestroyIcon.Call(uintptr(sh.icon))
		sh.icon = 0
	}
}

// trayRect asks the shell where the icon is, so the panel can open next to it.
func trayRect() (rect, bool) {
	id := notifyIconIdentifier{Hwnd: sh.hwndMain, ID: trayIconID}
	id.Size = uint32(unsafe.Sizeof(id))
	var r rect
	hr, _, _ := procShellNotifyIconGetRect.Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&r)))
	if hr != 0 || (r.Right == 0 && r.Bottom == 0) {
		return rect{}, false
	}
	return r, true
}

// buildTrayIcon renders the glyph in the colour the taskbar needs. The artwork
// is the macOS menu-bar template — pure black plus alpha, which macOS recolours
// itself. Windows does not, so the colour is applied here: black on a light
// taskbar, white on a dark one.
func buildTrayIcon(light bool) windows.Handle {
	img, err := png.Decode(bytes.NewReader(trayGlyph))
	if err != nil {
		h, _, _ := procLoadIconW.Call(0, idiApplication)
		return windows.Handle(h)
	}
	size := 16
	if v, _, _ := procGetSystemMetrics.Call(smCxSmIcon); v > 0 {
		size = int(v)
	}
	b := img.Bounds()
	sw, sh2 := b.Dx(), b.Dy()

	var c uint32 = 255
	if light {
		c = 0
	}

	pix := make([]byte, size*size*4) // BGRA, premultiplied, top-down
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// Bilinear sample of the alpha channel; the colour is constant.
			fx := (float64(x)+0.5)*float64(sw)/float64(size) - 0.5
			fy := (float64(y)+0.5)*float64(sh2)/float64(size) - 0.5
			a := bilinearAlpha(img, b.Min.X, b.Min.Y, sw, sh2, fx, fy)
			i := (y*size + x) * 4
			v := byte(float64(c) * a / 255)
			pix[i], pix[i+1], pix[i+2], pix[i+3] = v, v, v, byte(a)
		}
	}

	hdr := bitmapInfoHeader{Width: int32(size), Height: -int32(size), Planes: 1, BitCount: 32}
	hdr.Size = uint32(unsafe.Sizeof(hdr))
	var bits unsafe.Pointer
	color, _, _ := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&hdr)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if color == 0 || bits == nil {
		h, _, _ := procLoadIconW.Call(0, idiApplication)
		return windows.Handle(h)
	}
	copy(unsafe.Slice((*byte)(bits), len(pix)), pix)
	mask, _, _ := procCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, 0)

	ii := iconInfo{IsIcon: 1, Mask: windows.Handle(mask), Color: windows.Handle(color)}
	icon, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	_, _, _ = procDeleteObject.Call(color)
	_, _, _ = procDeleteObject.Call(mask)
	return windows.Handle(icon)
}

func bilinearAlpha(img image.Image, ox, oy, w, h int, fx, fy float64) float64 {
	x0, y0 := int(math.Floor(fx)), int(math.Floor(fy))
	tx, ty := fx-float64(x0), fy-float64(y0)
	at := func(x, y int) float64 {
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x >= w {
			x = w - 1
		}
		if y >= h {
			y = h - 1
		}
		_, _, _, a := img.At(ox+x, oy+y).RGBA()
		return float64(a >> 8)
	}
	top := at(x0, y0)*(1-tx) + at(x0+1, y0)*tx
	bot := at(x0, y0+1)*(1-tx) + at(x0+1, y0+1)*tx
	return top*(1-ty) + bot*ty
}

// showContextMenu is the right-click menu. The panel is the real interface; this
// is the escape hatch that still works when the panel cannot (no WebView2).
func showContextMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	add := func(id uintptr, label string) {
		_, _, _ = procAppendMenuW.Call(menu, mfString, id, uintptr(unsafe.Pointer(wstr(label))))
	}
	add(cmdOpen, "Open cix")
	add(cmdDashboard, "Open Dashboard")
	_, _, _ = procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	add(cmdQuit, "Quit cix (server keeps running)")

	var p point
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	// Required for the menu to dismiss when the user clicks elsewhere.
	_, _, _ = procSetForegroundWindow.Call(uintptr(sh.hwndMain))
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightButton|tpmBottomAlign,
		uintptr(p.X), uintptr(p.Y), 0, uintptr(sh.hwndMain), 0)
	switch cmd {
	case cmdOpen:
		openPanel()
	case cmdDashboard:
		if panelHooks.onAction != nil {
			go panelHooks.onAction(panelAction{Action: "dashboard"})
		}
	case cmdQuit:
		panelQuit()
	}
}

// handleCopyData runs a panel action sent by `cix-launcher -send <action>`.
//
// A test seam, nothing more, and OFF unless the RECEIVING process was started
// with CIX_TEST_CONTROL=1: it exists so the real action handlers (start, stop,
// the toggles) can be driven from a script on a machine where nobody can click.
// Anyone able to send this message can already edit server.env, but a switch
// that widens network exposure should not be reachable by default.
func handleCopyData(lp uintptr) uintptr {
	if os.Getenv("CIX_TEST_CONTROL") != "1" || lp == 0 {
		return 0
	}
	cds := (*copyDataStruct)(*(*unsafe.Pointer)(unsafe.Pointer(&lp)))
	if cds.Size == 0 || cds.Size > 256 {
		return 0
	}
	action := string(unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&cds.Data))), cds.Size))
	logf("test control: %s", action)
	a := panelAction{Action: action}
	// A JSON object carries a whole action, so a script can answer an in-panel
	// dialog: {"action":"dialog-result","ok":true}.
	if strings.HasPrefix(action, "{") {
		if err := json.Unmarshal([]byte(action), &a); err != nil {
			return 0
		}
	}
	if panelHooks.onAction != nil {
		go panelHooks.onAction(a)
	}
	return 1
}

type copyDataStruct struct {
	Flags uintptr
	Size  uint32
	Data  uintptr
}

// sendToRunningInstance delivers an action to the tray window of the running
// launcher. Returns false when there is none.
func sendToRunningInstance(action string) bool {
	h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(wstr(mainClass))), 0)
	if h == 0 {
		return false
	}
	b := []byte(action)
	cds := copyDataStruct{Size: uint32(len(b)), Data: uintptr(unsafe.Pointer(&b[0]))}
	r, _, _ := procSendMessageW.Call(h, wmCopyData, 0, uintptr(unsafe.Pointer(&cds)))
	return r != 0
}
