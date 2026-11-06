package main

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// Windows half of the runtime: the zip format and the `current` junction.
// Layout, manifest, download and checksum, pruning — runtime.go.
//
// Why a junction. macOS switches versions by renaming a symlink over `current`,
// which is atomic. Windows symlinks need either Developer Mode or elevation, and
// neither can be assumed; a directory junction needs neither, behaves like a
// symlink for every reader (the scheduled task, the PATH entry for the CLI), and
// can be created from an unprivileged process. What it lacks is rename-over:
// MoveFileEx will not replace a directory. activateRuntime therefore moves the
// old link aside and the new one in — two renames, with a window of microseconds
// in which `current` does not exist. The server is stopped for the swap anyway
// (updateRuntime), so nothing is reading it.

// The asset's name. Set by win/scripts/build-runtime.sh.
const runtimeAssetSuffix = "-windows-amd64.zip"

// exeSuffix is appended to every binary name.
const exeSuffix = ".exe"

// resolveRuntimeLink returns the directory the `current` junction points at.
//
// The launcher must not walk through its own junction. Windows 11 treats a
// junction created by a non-administrator as untrusted (Redirection Guard), and a
// process that has opted in — setup programs and anything they launch, some
// service hosts, an OpenSSH session — gets "the path cannot be traversed because
// it contains an untrusted mount point" for every path through it. The launcher
// inherits that when the installer relaunches it after an update, and would then
// conclude that no runtime is installed. Reading the link itself is always
// allowed, so the launcher resolves it once and uses the real versioned path.
//
// The junction still earns its place: the scheduled task and the user's PATH
// entry go through it, and neither of those is an opted-in process.
func resolveRuntimeLink(link string) string {
	target, err := os.Readlink(link)
	if err != nil || target == "" {
		return link
	}
	return strings.TrimPrefix(target, `\\?\`)
}

// currentRuntimeVersion reads the junction, returning "" when nothing is
// installed. It reads the link rather than the manifest so that a runtime whose
// manifest is missing or unreadable is still identifiable.
func currentRuntimeVersion() string {
	link, err := runtimeCurrentDir()
	if err != nil {
		return ""
	}
	target, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimRight(target, `\/`))
}

// runtimeReady reports whether the current runtime is usable — the junction
// resolves and the server binary is actually there.
func runtimeReady() bool {
	server, err := runtimeLiveServerPath()
	if err != nil {
		return false
	}
	info, err := os.Stat(server)
	return err == nil && !info.IsDir()
}

// unpackRuntime extracts a runtime zip into dest, then proves the result is
// runnable before letting anything point at it. Extraction goes to a sibling
// staging directory and is renamed into place, so an interrupted install can
// never leave a half-populated version directory.
func unpackRuntime(archive, dest string) error {
	root := filepath.Dir(dest)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(root, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	if err := extractZip(archive, staging); err != nil {
		return fmt.Errorf("could not unpack the runtime: %w", err)
	}

	entries, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	tree := staging
	if len(entries) == 1 && entries[0].IsDir() && strings.HasPrefix(entries[0].Name(), runtimeDirPrefix) {
		tree = filepath.Join(staging, entries[0].Name())
	}

	if err := verifyRuntimeTree(tree); err != nil {
		return err
	}

	old := ""
	if _, err := os.Stat(dest); err == nil {
		old = dest + ".replaced"
		os.RemoveAll(old)
		if err := os.Rename(dest, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tree, dest); err != nil {
		if old != "" {
			os.Rename(old, dest)
		}
		return err
	}
	if old != "" {
		os.RemoveAll(old)
	}
	return nil
}

// verifyRuntimeTree checks an unpacked runtime the way a stranger would: the
// binaries are all there, and the server actually runs on this machine.
func verifyRuntimeTree(tree string) error {
	for _, rel := range []string{"cix-server.exe", "cix.exe", filepath.Join("llama", "llama-server.exe")} {
		if _, err := os.Stat(filepath.Join(tree, rel)); err != nil {
			return fmt.Errorf("the runtime is incomplete: %s is missing", rel)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := hiddenCommand(ctx, filepath.Join(tree, "cix-server.exe"), "-v").CombinedOutput()
	if err != nil {
		return fmt.Errorf("the downloaded server does not run on this PC: %v %s", err, strings.TrimSpace(string(out)))
	}
	logf("runtime staged at %s reports: %s", tree, strings.TrimSpace(string(out)))
	return nil
}

// extractZip unpacks a zip into dir. Written against archive/zip with the same
// containment rule as the tarball reader: an entry named ..\..\x is a valid
// archive and a directory traversal.
func extractZip(path, dir string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		// Zip names use forward slashes; reject anything that is not plain.
		if strings.Contains(f.Name, `\`) || strings.Contains(f.Name, ":") || strings.HasPrefix(f.Name, "/") {
			return fmt.Errorf("archive entry has an unexpected path: %s", f.Name)
		}
		target, err := safeJoin(dir, filepath.FromSlash(f.Name))
		if err != nil {
			return err
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := copyZipEntry(f, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected entry type in the runtime archive: %s", f.Name)
		}
	}
	return nil
}

func copyZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// activateRuntime points `current` at a version and returns the version it
// replaced, so the caller can put it back.
func activateRuntime(version string) (previous string, err error) {
	root, err := runtimeRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, version)
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("runtime %s is not installed", version)
	}

	previous = currentRuntimeVersion()

	link := filepath.Join(root, runtimeLinkName)
	fresh := filepath.Join(root, ".current.new")
	aside := filepath.Join(root, ".current.old")
	removeJunction(fresh)
	removeJunction(aside)

	if err := makeJunction(fresh, dir); err != nil {
		return previous, fmt.Errorf("could not create the runtime link: %w", err)
	}
	hadLink := false
	if _, err := os.Lstat(link); err == nil {
		hadLink = true
		if err := os.Rename(link, aside); err != nil {
			removeJunction(fresh)
			return previous, err
		}
	}
	if err := os.Rename(fresh, link); err != nil {
		if hadLink {
			os.Rename(aside, link)
		}
		removeJunction(fresh)
		return previous, err
	}
	removeJunction(aside)
	logf("runtime %s activated (was %q)", version, previous)
	return previous, nil
}

// removeJunction deletes a junction (or a plain empty directory) without
// following it: os.Remove maps to RemoveDirectory, which removes the link and
// leaves its target alone.
func removeJunction(path string) {
	if _, err := os.Lstat(path); err == nil {
		_ = os.Remove(path)
	}
}

// makeJunction creates a directory junction at link pointing to target, with no
// elevation: it writes an IO_REPARSE_TAG_MOUNT_POINT reparse point onto an empty
// directory.
func makeJunction(link, target string) error {
	target, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if err := os.Mkdir(link, 0o755); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(link)
		}
	}()

	sub := utf16.Encode([]rune(`\??\` + target))
	print := utf16.Encode([]rune(target))
	// REPARSE_DATA_BUFFER for a mount point: header, four offsets/lengths, then
	// PathBuffer holding SubstituteName and PrintName, each NUL-terminated.
	pathBytes := (len(sub) + 1 + len(print) + 1) * 2
	buf := make([]byte, 8+8+pathBytes)
	binary.LittleEndian.PutUint32(buf[0:], 0xA0000003) // IO_REPARSE_TAG_MOUNT_POINT
	binary.LittleEndian.PutUint16(buf[4:], uint16(8+pathBytes))
	binary.LittleEndian.PutUint16(buf[8:], 0)                       // SubstituteNameOffset
	binary.LittleEndian.PutUint16(buf[10:], uint16(len(sub)*2))     // SubstituteNameLength
	binary.LittleEndian.PutUint16(buf[12:], uint16((len(sub)+1)*2)) // PrintNameOffset
	binary.LittleEndian.PutUint16(buf[14:], uint16(len(print)*2))   // PrintNameLength
	off := 16
	for _, c := range sub {
		binary.LittleEndian.PutUint16(buf[off:], c)
		off += 2
	}
	off += 2
	for _, c := range print {
		binary.LittleEndian.PutUint16(buf[off:], c)
		off += 2
	}

	h, err := windows.CreateFile(windows.StringToUTF16Ptr(link), windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)

	var returned uint32
	const fsctlSetReparsePoint = 0x000900A4
	if err := windows.DeviceIoControl(h, fsctlSetReparsePoint, &buf[0], uint32(len(buf)), nil, 0, &returned, nil); err != nil {
		return err
	}
	ok = true
	return nil
}
