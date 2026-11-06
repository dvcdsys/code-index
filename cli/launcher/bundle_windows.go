package main

import (
	"os"
	"path/filepath"
)

// bundle describes the installation this launcher is running from.
//
// There is no bundle on Windows — the installer drops cix-launcher.exe and
// nothing else that runs into %LOCALAPPDATA%\Programs\cix. The type exists so
// the shared code (updater, menu) can hold "where am I" the same way on both
// platforms.
type bundle struct {
	Root      string // the install directory
	Resources string // same directory; nothing is nested on Windows
}

// locateBundle derives the layout from the running executable. It cannot fail
// for being in the wrong place: running from a build directory is a developer
// scenario, and refusing it would only get in the way.
func locateBundle() (bundle, error) {
	exe, err := os.Executable()
	if err != nil {
		return bundle{}, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	return bundle{Root: dir, Resources: dir}, nil
}
