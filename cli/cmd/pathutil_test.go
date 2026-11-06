package cmd

import (
	"runtime"
	"testing"
)

func TestIsUnderRoot(t *testing.T) {
	cases := []struct {
		path, root string
		want       bool
	}{
		{"/home/u/proj/src", "/home/u/proj", true},
		{"/home/u/proj", "/home/u/proj", false}, // equal is samePath's job
		{"/home/u/project", "/home/u/proj", false},
	}
	for _, c := range cases {
		if got := isUnderRoot(c.path, c.root); got != c.want {
			t.Errorf("isUnderRoot(%q, %q) = %v, want %v", c.path, c.root, got, c.want)
		}
	}
	if runtime.GOOS == "windows" {
		for _, c := range []struct {
			path, root string
			want       bool
		}{
			{`C:\Users\me\proj\src\api`, `C:\Users\me\proj`, true},
			{`c:\users\ME\proj\src`, `C:\Users\me\proj`, true},
			{`C:/Users/me/proj/src`, `C:\Users\me\proj`, true},
			{`C:\Users\me\project`, `C:\Users\me\proj`, false},
		} {
			if got := isUnderRoot(c.path, c.root); got != c.want {
				t.Errorf("isUnderRoot(%q, %q) = %v, want %v", c.path, c.root, got, c.want)
			}
		}
		if !samePath(`C:\Users\Me\proj`, `c:/users/me/proj`) {
			t.Error("samePath should ignore case and separator style on Windows")
		}
	}
}

// skipOnWindows skips a test that models POSIX absolute paths ("/repo/a/…").
// Such a path is not absolute on Windows, so the code under test legitimately
// takes a different branch there; the Windows behaviour is covered by
// TestIsUnderRoot and by the tests that build their paths from t.TempDir().
func skipOnWindows(t *testing.T, why string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip(why)
	}
}
