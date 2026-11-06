package main

import "testing"

func TestIsUserCancelled(t *testing.T) {
	// The message is localised and the spelling differs between locales — this
	// project's own development Mac runs en-GB and emits "cancelled", while the
	// American spelling has one L. Matching either sentence is wrong somewhere,
	// and was: every dialog dismissal in the app was being reported as a
	// failure. The OSStatus is the same number in every language.
	cancelled := []string{
		`0:778: execution error: User cancelled. (-128)`,
		`0:112: execution error: User canceled. (-128)`,
		`0:50: execution error: User canceled the operation. (-128)`,
	}
	for _, s := range cancelled {
		if !isUserCancelled(s) {
			t.Errorf("isUserCancelled(%q) = false, want true", s)
		}
	}

	notCancelled := []string{
		"",
		`0:10: syntax error: Expected end of line but found identifier. (-2741)`,
		`execution error: Finder got an error: Can't get disk "cix". (-1728)`,
	}
	for _, s := range notCancelled {
		if isUserCancelled(s) {
			t.Errorf("isUserCancelled(%q) = true, want false", s)
		}
	}
}

func TestParseMountPoint(t *testing.T) {
	// hdiutil prints one line per partition; the volume is on the last one.
	out := "/dev/disk4          \tGUID_partition_scheme          \t\n" +
		"/dev/disk4s1        \tApple_HFS                      \t/Volumes/cix\n"
	if got, want := parseMountPoint(out), "/Volumes/cix"; got != want {
		t.Errorf("parseMountPoint() = %q, want %q", got, want)
	}
	if got := parseMountPoint("/dev/disk9\tGUID_partition_scheme\t\n"); got != "" {
		t.Errorf("parseMountPoint() with no volume = %q, want empty", got)
	}
}

func TestCheckWritable(t *testing.T) {
	if err := checkWritable(t.TempDir()); err != nil {
		t.Errorf("checkWritable on a temp dir = %v, want nil", err)
	}
	// The point of the preflight: find out before spending a download, while
	// the installed app is still untouched and the advice can be "drag the new
	// one over the old one".
	if err := checkWritable("/usr/bin"); err == nil {
		t.Skip("running as root, or /usr/bin is writable — preflight cannot be exercised")
	}
}
