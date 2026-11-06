package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// cix-launcher — the executable behind the Windows tray app.
//
// A notification-area icon showing whether the local cix server is running and
// what its embedding provider is doing, with start/stop and a dashboard link. No
// taskbar button and no window until the icon is clicked.
//
// Built with -H=windowsgui, so it has no console of its own. -v and -report are
// the scriptable paths and print to the console that started them.
func main() {
	showVersion := flag.Bool("v", false, "print version and exit")
	report := flag.Bool("report", false, "print an installation report to stdout and exit, instead of showing the tray icon")
	breakPID := flag.Int("break", 0, "internal: send Ctrl+Break to the console of this pid and exit")
	send := flag.String("send", "", "test seam: send a panel action to the running app (needs CIX_TEST_CONTROL=1 there) and exit")
	flag.Parse()

	if *breakPID != 0 {
		os.Exit(breakHelper(*breakPID))
	}
	if *send != "" {
		if !sendToRunningInstance(*send) {
			os.Exit(1)
		}
		return
	}

	if *showVersion || *report {
		attachParentConsole()
	}
	if *showVersion {
		fmt.Printf("cix-launcher %s\n", version)
		return
	}

	b, err := locateBundle()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cix-launcher %s: %v\n", version, err)
		os.Exit(1)
	}

	if *report {
		fmt.Println(bundleReport(b))
		return
	}

	// One instance. A second launch (Start menu, the installer's relaunch) asks
	// the first to open its panel and exits — what LaunchServices does for free
	// on macOS.
	if !acquireSingleton() {
		if h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(wstr(mainClass))), 0); h != 0 {
			postMessage(windows.HWND(h), wmOpenPanel, 0, 0)
		}
		return
	}

	u := newUpdater(b)
	runMenu(b, u)
}

// singletonName doubles as the installer's AppMutex: while this exists, setup
// knows the app is running and waits for it to exit.
const singletonName = "cix-launcher-singleton"

func acquireSingleton() bool {
	h, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(singletonName))
	if err != nil && h == 0 {
		return true // cannot tell; better two trays than none
	}
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(h)
		return false
	}
	return true // the handle is held for the life of the process
}

// attachParentConsole routes stdout/stderr to the console that launched us. A
// windowsgui binary has none, and fmt.Println would vanish.
func attachParentConsole() {
	// A redirect (`> file`, a pipe, Start-Process -RedirectStandardOutput) has
	// already given us a working handle; borrowing the console would send the
	// output past it.
	if h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil && h != 0 && h != windows.InvalidHandle {
		if t, _ := windows.GetFileType(h); t != windows.FILE_TYPE_UNKNOWN {
			return
		}
	}
	const attachParent = ^uintptr(0) // ATTACH_PARENT_PROCESS = -1
	if r, _, _ := procAttachConsole.Call(attachParent); r == 0 {
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = out, out
	}
}

// bundleReport prints what this installation actually consists of: the app, and
// whichever runtime it is pointing at. It asks the binaries for their own
// versions rather than trusting runtime.json — see the macOS version for why.
func bundleReport(b bundle) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Launcher:   cix-launcher %s\n", version)
	fmt.Fprintf(&sb, "Install:    %s\n", b.Root)

	if !runtimeReady() {
		sb.WriteString("Runtime:    not installed\n")
		return sb.String()
	}

	dir, _ := runtimeLiveDir()
	fmt.Fprintf(&sb, "Runtime:    %s (%s)\n", currentRuntimeVersion(), dir)
	fmt.Fprintf(&sb, "Server:     %s\n", binaryVersion(filepath.Join(dir, "cix-server.exe"), "-v"))
	fmt.Fprintf(&sb, "CLI:        %s\n", binaryVersion(filepath.Join(dir, "cix.exe"), "--version"))

	llama := filepath.Join(dir, "llama", "llama-server.exe")
	if fileExists(llama) {
		info, _ := readRuntimeInfo()
		cuda := ""
		if info.CUDA != "" {
			cuda = " (CUDA " + info.CUDA + ")"
		}
		fmt.Fprintf(&sb, "Embeddings: llama-server %s%s\n", info.LlamaVersion, cuda)
	} else {
		fmt.Fprintf(&sb, "Embeddings: MISSING — %s not found\n", llama)
	}
	return sb.String()
}
