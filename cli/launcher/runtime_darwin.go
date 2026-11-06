package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// macOS half of the runtime: the tarball format, the `current` symlink, and the
// code-signature checks. The platform-neutral rest — layout, manifest, download
// and checksum, pruning — is in runtime.go. Read that file's header for why the
// runtime lives outside the app and why it is switched by renaming a link.

// The asset's name. Set by mac/scripts/build-runtime.sh.
const runtimeAssetSuffix = "-darwin-arm64.tar.gz"

// resolveRuntimeLink is the identity on macOS: a symlink is followed by every
// process, so the launcher can use the stable path directly.
func resolveRuntimeLink(link string) string { return link }

// exeSuffix is appended to every binary name; none on macOS.
const exeSuffix = ""

// currentRuntimeVersion reads the symlink, returning "" when nothing is
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
	return filepath.Base(target)
}

// runtimeReady reports whether the current runtime is usable — the symlink
// resolves and the server binary is actually there. Both halves matter: a
// symlink pointing at a pruned directory reads as installed until something
// tries to exec it.
func runtimeReady() bool {
	server, err := runtimeServerPath()
	if err != nil {
		return false
	}
	info, err := os.Stat(server)
	return err == nil && !info.IsDir()
}

// unpackRuntime extracts a runtime tarball into dest, then proves the result is
// runnable before letting anything point at it.
//
// Extraction goes to a sibling temp directory and is renamed into place, so an
// interrupted install can never leave a half-populated version directory that
// looks complete to currentRuntimeVersion.
func unpackRuntime(tarball, dest string) error {
	root := filepath.Dir(dest)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}

	staging, err := os.MkdirTemp(root, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	if err := extractTarGz(tarball, staging); err != nil {
		return fmt.Errorf("could not unpack the runtime: %w", err)
	}

	// The archive carries one top-level directory. Descend into it rather than
	// trusting its name: the version in the filename and the version in the
	// release tag have been the same so far, but nothing enforces it.
	entries, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	tree := staging
	if len(entries) == 1 && entries[0].IsDir() && strings.HasPrefix(entries[0].Name(), runtimeDirPrefix) {
		tree = filepath.Join(staging, entries[0].Name())
	}

	// Quarantine is not applied to files this process downloads over HTTP, but
	// it costs nothing to be certain: a quarantined llama-server is SIGKILLed on
	// exec with EMPTY STDERR, which is indistinguishable from a crash and has
	// already cost this project a debugging session (see server/Makefile).
	_ = exec.Command("xattr", "-cr", tree).Run()

	if err := verifyRuntimeTree(tree); err != nil {
		return err
	}

	// Replace atomically-ish: the old directory is moved aside, the new one
	// renamed in, and only then is the old one deleted. A reinstall of the
	// version currently in use is the case this protects.
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

// verifyRuntimeTree checks an unpacked runtime the way a stranger would.
//
// codesign --verify says the signature is well-formed; only exec proves the
// kernel agrees, and that is the check that matters, because the failure it
// catches — an ad-hoc signature the kernel rejects — kills the process with no
// output at all.
func verifyRuntimeTree(tree string) error {
	for _, rel := range []string{"cix-server", "cix", filepath.Join("llama", "llama-server")} {
		path := filepath.Join(tree, rel)
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("the runtime is incomplete: %s is missing", rel)
		}
		if out, err := exec.Command("codesign", "--verify", "--strict", path).CombinedOutput(); err != nil {
			return fmt.Errorf("%s failed signature verification and was discarded: %s",
				rel, strings.TrimSpace(string(out)))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, filepath.Join(tree, "cix-server"), "-v").CombinedOutput()
	if err != nil {
		return fmt.Errorf("the downloaded server does not run on this Mac: %v %s", err, strings.TrimSpace(string(out)))
	}
	logf("runtime staged at %s reports: %s", tree, strings.TrimSpace(string(out)))
	return nil
}

// extractTarGz unpacks a gzipped tar into dir.
//
// Written against archive/tar rather than shelling out to /usr/bin/tar for one
// reason that matters: path containment. A tar member named ../../something is
// a valid archive and a directory traversal, and the check belongs where the
// paths are joined.
func extractTarGz(path, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		target, err := safeJoin(dir, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			// The execute bit is the payload here — cix-server, cix and
			// llama-server all arrive as 0755 and are useless without it.
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode).Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			// Containment applies to what the link points at as well: a symlink
			// escaping the tree would let a later member be written outside it.
			if _, err := safeJoin(dir, filepath.Join(filepath.Dir(hdr.Name), hdr.Linkname)); err != nil {
				return fmt.Errorf("archive symlink escapes the destination: %s -> %s", hdr.Name, hdr.Linkname)
			}
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			// Device nodes, fifos, hard links: nothing this archive should
			// contain, so refuse rather than silently skip.
			return fmt.Errorf("unexpected entry type %q in the runtime archive: %s", hdr.Typeflag, hdr.Name)
		}
	}
}

// activateRuntime points `current` at a version and returns the version it
// replaced, so the caller can put it back.
//
// The rename is what makes this safe: rename(2) over an existing symlink is
// atomic, so there is no instant at which `current` is missing or dangling.
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

	tmp := filepath.Join(root, ".current.new")
	os.Remove(tmp)
	if err := os.Symlink(version, tmp); err != nil {
		return previous, err
	}
	if err := os.Rename(tmp, filepath.Join(root, runtimeLinkName)); err != nil {
		os.Remove(tmp)
		return previous, err
	}
	logf("runtime %s activated (was %q)", version, previous)
	return previous, nil
}
