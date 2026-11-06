package cmd

import (
	"path/filepath"
	"runtime"
	"strings"
)

// isUnderRoot reports whether path lies strictly inside root.
//
// Unix compares the exact strings, as the registered host_path values are
// opaque identifiers there. On Windows the same directory has several spellings
// — `C:\proj\src` and `c:/proj/src` are one place — so both sides are
// slash-normalised and compared case-insensitively. A hard-coded "/" suffix
// (the previous check) never matches a backslash path, so `cix search` from a
// subdirectory could not find its project root.
func isUnderRoot(path, root string) bool {
	if runtime.GOOS == "windows" {
		path = strings.ToLower(filepath.ToSlash(path))
		root = strings.ToLower(filepath.ToSlash(root))
	}
	root = strings.TrimSuffix(root, "/")
	return root != "" && strings.HasPrefix(path, root+"/")
}

// samePath reports whether two paths name the same location (see isUnderRoot).
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.ToSlash(a), filepath.ToSlash(b))
	}
	return a == b
}
