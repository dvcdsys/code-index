package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/cli/internal/release"
)

// The runtime is downloaded from the internet and unpacked into the user's home
// directory, and the symlink swap is what a failed update has to reverse. Both
// are worth testing without a network, a signed binary, or a real release.

func TestSafeJoinRejectsEscapes(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{
		"../escape",
		"a/../../escape",
		"/absolute",
		"cix-runtime-1.0.0/../../escape",
	} {
		if got, err := safeJoin(dir, name); err == nil {
			t.Errorf("safeJoin(%q) = %q, want an error", name, got)
		}
	}

	for _, name := range []string{"cix-server", "llama/llama-server", "./cix"} {
		if _, err := safeJoin(dir, name); err != nil {
			t.Errorf("safeJoin(%q) returned %v, want it accepted", name, err)
		}
	}
}

// writeTarGz builds an archive in memory. Each entry is name → content; a name
// ending in "/" becomes a directory.
func writeTarGz(t *testing.T, path string, entries map[string]string, modes map[string]int64) {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, content := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if m, ok := modes[name]; ok {
			hdr.Mode = m
		}
		if len(name) > 0 && name[len(name)-1] == '/' {
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
			hdr.Size = 0
		} else {
			hdr.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A tarball is an untrusted input even when we produced the one that normally
// arrives: the checksum proves it matches the release, and the release is served
// over the same channel as everything else. Containment is checked here rather
// than assumed.
func TestExtractTarGzRefusesTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.tar.gz")
	writeTarGz(t, archive, map[string]string{"../escaped": "owned"}, nil)

	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(archive, dest); err == nil {
		t.Fatal("extractTarGz accepted an entry escaping the destination")
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped")); err == nil {
		t.Fatal("extractTarGz wrote outside the destination directory")
	}
}

// The execute bit is the payload: a cix-server extracted at 0644 is a runtime
// that installs cleanly and cannot be started.
func TestExtractTarGzPreservesExecuteBit(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "runtime.tar.gz")
	writeTarGz(t,
		archive,
		map[string]string{
			"cix-runtime-1.0.0/":                   "",
			"cix-runtime-1.0.0/cix-server":         "#!/bin/sh\n",
			"cix-runtime-1.0.0/runtime.json":       `{"runtime_version":"1.0.0"}`,
			"cix-runtime-1.0.0/llama/llama-server": "#!/bin/sh\n",
		},
		map[string]int64{
			"cix-runtime-1.0.0/cix-server":         0o755,
			"cix-runtime-1.0.0/llama/llama-server": 0o755,
		})

	dest := filepath.Join(dir, "out")
	if err := extractTarGz(archive, dest); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"cix-server", "llama/llama-server"} {
		info, err := os.Stat(filepath.Join(dest, "cix-runtime-1.0.0", rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s extracted as %v, want it executable", rel, info.Mode().Perm())
		}
	}

	info, err := os.Stat(filepath.Join(dest, "cix-runtime-1.0.0", "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 != 0 {
		t.Errorf("runtime.json extracted as %v, want it non-executable", info.Mode().Perm())
	}
}

// installRuntimeDir fakes an installed version, without the signed binaries a
// real one carries.
func installRuntimeDir(t *testing.T, home, version string) string {
	t.Helper()
	dir := filepath.Join(home, ".cix", "runtime", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cix-server"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestActivateRuntimeSwapsAndReports(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	installRuntimeDir(t, home, "1.0.0")
	installRuntimeDir(t, home, "1.1.0")

	if v := currentRuntimeVersion(); v != "" {
		t.Fatalf("currentRuntimeVersion() = %q before any activation, want empty", v)
	}
	if runtimeReady() {
		t.Fatal("runtimeReady() is true with no current symlink")
	}

	previous, err := activateRuntime("1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if previous != "" {
		t.Errorf("first activation reported previous = %q, want empty", previous)
	}
	if got := currentRuntimeVersion(); got != "1.0.0" {
		t.Fatalf("currentRuntimeVersion() = %q, want 1.0.0", got)
	}
	if !runtimeReady() {
		t.Fatal("runtimeReady() is false after activating a complete runtime")
	}

	// The swap has to work over an existing symlink — that is the update path,
	// and os.Symlink alone would fail with EEXIST.
	previous, err = activateRuntime("1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if previous != "1.0.0" {
		t.Errorf("activateRuntime reported previous = %q, want 1.0.0", previous)
	}
	if got := currentRuntimeVersion(); got != "1.1.0" {
		t.Fatalf("currentRuntimeVersion() = %q, want 1.1.0", got)
	}

	// Rollback is the same operation in reverse, which is the point of using a
	// symlink at all.
	if _, err := activateRuntime(previous); err != nil {
		t.Fatal(err)
	}
	if got := currentRuntimeVersion(); got != "1.0.0" {
		t.Fatalf("after rollback currentRuntimeVersion() = %q, want 1.0.0", got)
	}

	// The link must stay relative, so the tree survives a moved home directory.
	link, _ := runtimeCurrentDir()
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(target) {
		t.Errorf("current -> %q is absolute, want a relative target", target)
	}
}

func TestActivateRuntimeRefusesMissingVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installRuntimeDir(t, home, "1.0.0")

	if _, err := activateRuntime("1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := activateRuntime("9.9.9"); err == nil {
		t.Fatal("activateRuntime accepted a version that is not installed")
	}
	// A refused activation must not have disturbed the working one.
	if got := currentRuntimeVersion(); got != "1.0.0" {
		t.Fatalf("currentRuntimeVersion() = %q after a failed activation, want 1.0.0", got)
	}
}

func TestPruneRuntimesKeepsCurrentAndPrevious(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0"} {
		installRuntimeDir(t, home, v)
	}
	if _, err := activateRuntime("1.3.0"); err != nil {
		t.Fatal(err)
	}

	pruneRuntimes("1.3.0", "1.2.0")

	root := filepath.Join(home, ".cix", "runtime")
	for _, v := range []string{"1.3.0", "1.2.0"} {
		if _, err := os.Stat(filepath.Join(root, v)); err != nil {
			t.Errorf("%s was pruned, want it kept: %v", v, err)
		}
	}
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if _, err := os.Stat(filepath.Join(root, v)); err == nil {
			t.Errorf("%s survived the prune, want it removed", v)
		}
	}
	// Pruning must never take the symlink with it.
	if got := currentRuntimeVersion(); got != "1.3.0" {
		t.Fatalf("currentRuntimeVersion() = %q after pruning, want 1.3.0", got)
	}
}

func TestRuntimeAssetName(t *testing.T) {
	if got, want := runtimeAssetName("0.12.8"), "cix-runtime-0.12.8-darwin-arm64.tar.gz"; got != want {
		t.Errorf("runtimeAssetName() = %q, want %q", got, want)
	}
	// The round trip matters: the local-tarball path names the install directory
	// by reading the version back out of the filename.
	if got, want := runtimeVersionFromTarball("/tmp/cix-runtime-0.12.8-darwin-arm64.tar.gz"), "0.12.8"; got != want {
		t.Errorf("runtimeVersionFromTarball() = %q, want %q", got, want)
	}
	if got := runtimeVersionFromTarball("/tmp/something-else.tar.gz"); got != "local" {
		t.Errorf("runtimeVersionFromTarball() on an unrecognised name = %q, want local", got)
	}
}

func TestRuntimeSummaryWithoutInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got, want := runtimeSummary(), "Server: not installed"; got != want {
		t.Errorf("runtimeSummary() = %q, want %q", got, want)
	}
}

// fakeRuntimeTarball builds a runtime that is structurally real: three ad-hoc
// signed Mach-O executables and a manifest, packed the way build-runtime.sh
// packs one.
//
// /bin/echo stands in for the binaries because the checks being exercised are
// real ones — codesign --verify --strict, and an actual exec of `cix-server -v`
// — and stubbing them out would test nothing. A copy of a system binary can be
// re-signed ad-hoc; the SIP-protected original could not.
func fakeRuntimeTarball(t *testing.T, dir, version string) string {
	t.Helper()

	tree := filepath.Join(dir, runtimeDirPrefix+version)
	if err := os.MkdirAll(filepath.Join(tree, "llama"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"cix-server", "cix", "llama/llama-server"} {
		dst := filepath.Join(tree, rel)
		src, err := os.ReadFile("/bin/echo")
		if err != nil {
			t.Skipf("cannot read /bin/echo: %v", err)
		}
		if err := os.WriteFile(dst, src, 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("codesign", "--force", "--sign", "-", dst).CombinedOutput(); err != nil {
			t.Skipf("codesign unavailable: %v %s", err, out)
		}
	}
	manifest := `{"server_version":"` + version + `","cli_version":"8.8.8","llama_version":"bTEST","platform":"darwin-arm64"}`
	if err := os.WriteFile(filepath.Join(tree, runtimeManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	tarball := filepath.Join(dir, runtimeAssetName(version))
	cmd := exec.Command("tar", "-czf", tarball, "-C", dir, runtimeDirPrefix+version)
	cmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tar: %v %s", err, out)
	}
	return tarball
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// serveRelease publishes a tarball and its checksums the way a GitHub release
// does, and returns the Release the updater would have been handed.
func serveRelease(t *testing.T, version, tarball string, corruptChecksum bool) release.Release {
	t.Helper()

	sum := sha256File(t, tarball)
	if corruptChecksum {
		sum = strings.Repeat("0", 64)
	}
	sums := sum + "  ./" + filepath.Base(tarball) + "\n"

	mux := http.NewServeMux()
	mux.HandleFunc("/runtime", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, tarball)
	})
	mux.HandleFunc("/checksums", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, sums)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return release.Release{
		Version: version,
		TagName: serverTagPrefix + version,
		Assets: []release.Asset{
			{Name: runtimeAssetName(version), URL: srv.URL + "/runtime"},
			{Name: "checksums.txt", URL: srv.URL + "/checksums"},
		},
	}
}

// The whole install path, end to end: download, checksum, unpack, strip
// quarantine, verify every signature, exec the server, then activate.
func TestInstallRuntimeEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tarball := fakeRuntimeTarball(t, t.TempDir(), "1.0.0")
	rel := serveRelease(t, "1.0.0", tarball, false)

	if err := installRuntime(rel, func(string) {}); err != nil {
		t.Fatalf("installRuntime: %v", err)
	}
	// Installing must not switch to it: everything fallible happens before the
	// running system is touched, and the swap is a separate, reversible step.
	if v := currentRuntimeVersion(); v != "" {
		t.Fatalf("installRuntime activated %q on its own", v)
	}

	if _, err := activateRuntime("1.0.0"); err != nil {
		t.Fatal(err)
	}
	if !runtimeReady() {
		t.Fatal("runtimeReady() is false after a successful install")
	}

	info, err := readRuntimeInfo()
	if err != nil {
		t.Fatalf("readRuntimeInfo: %v", err)
	}
	if info.ServerVersion != "1.0.0" || info.LlamaVersion != "bTEST" {
		t.Errorf("manifest = %+v, want server 1.0.0 / llama bTEST", info)
	}
	if got, want := runtimeSummary(), "Server 1.0.0 (llama bTEST)"; got != want {
		t.Errorf("runtimeSummary() = %q, want %q", got, want)
	}

	// The download cache is temporary by design — a 35 MB tarball has no reason
	// to outlive the install that used it.
	cache, _ := updatesCacheDir()
	if _, err := os.Stat(cache); err == nil {
		t.Errorf("%s survived the install, want it removed", cache)
	}
}

// Without a Developer ID signature the checksum is the entire integrity story,
// so a mismatch has to stop the install rather than warn about it.
func TestInstallRuntimeRefusesBadChecksum(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tarball := fakeRuntimeTarball(t, t.TempDir(), "1.0.0")
	rel := serveRelease(t, "1.0.0", tarball, true)

	if err := installRuntime(rel, func(string) {}); err == nil {
		t.Fatal("installRuntime accepted a tarball that failed its checksum")
	}
	if _, err := os.Stat(filepath.Join(home, ".cix", "runtime", "1.0.0")); err == nil {
		t.Error("a version directory was created despite the failed checksum")
	}
}

// An incomplete runtime must be rejected before anything can point at it —
// codesign --verify passes on the files that are present, so completeness is a
// separate check and worth its own test.
func TestUnpackRuntimeRejectsIncompleteTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	staging := t.TempDir()
	tarball := fakeRuntimeTarball(t, staging, "1.0.0")
	// Repack without the CLI.
	tree := filepath.Join(staging, runtimeDirPrefix+"1.0.0")
	if err := os.Remove(filepath.Join(tree, "cix")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("tar", "-czf", tarball, "-C", staging, runtimeDirPrefix+"1.0.0")
	cmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tar: %v %s", err, out)
	}

	dest := filepath.Join(home, ".cix", "runtime", "1.0.0")
	err := unpackRuntime(tarball, dest)
	if err == nil {
		t.Fatal("unpackRuntime accepted a runtime with no cix CLI")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("error = %q, want it to say the runtime is incomplete", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("the version directory was created despite the incomplete tree")
	}
}

// serveStreamListing serves the releases API for the server/v* stream, honouring
// If-None-Match the way GitHub does. Returns the base URL and a counter of the
// 200s it answered.
func serveStreamListing(t *testing.T, version, etag string) (base string, full *int) {
	t.Helper()

	served := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/dvcdsys/code-index/releases", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		served++
		w.Header().Set("ETag", etag)
		_, _ = io.WriteString(w, `[{"tag_name":"`+serverTagPrefix+version+`","html_url":"https://example.invalid","assets":[]}]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, &served
}

// A cached ETag says the release *listing* has not changed. It says nothing
// about whether this machine has a runtime, so an install must not read a 304 as
// "there is no server to install" — that turned every launch after the first
// into a permanently unsetuppable app, since the ETag is persisted in
// launcher.json and survives reinstalling.
func TestLatestRuntimeIgnoresAStaleETag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const etag = `"cached-from-a-previous-run"`
	base, served := serveStreamListing(t, "1.2.3", etag)
	t.Setenv("CIX_UPDATE_BASE_URL", base)

	// What the previous run left behind: an ETag, and no memory of the body it
	// matched.
	if err := savePrefs(prefs{RuntimeETag: etag}); err != nil {
		t.Fatal(err)
	}

	u := newUpdater(bundle{})
	rel, err := u.latestRuntime(context.Background())
	if err != nil {
		t.Fatalf("latestRuntime: %v", err)
	}
	if rel.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", rel.Version)
	}
	if *served != 1 {
		t.Errorf("full listings served = %d, want 1 (the retry without the ETag)", *served)
	}
	// The retry runs on a copy: the cached ETag is what keeps the routine checks
	// off GitHub's rate limit, and an install must not spend it.
	if u.runtime.ETag != etag {
		t.Errorf("cached ETag = %q, want it left at %q", u.runtime.ETag, etag)
	}
	if p := loadPrefs(); p.RuntimeETag != etag {
		t.Errorf("persisted ETag = %q, want it left at %q", p.RuntimeETag, etag)
	}
}

// When this process already listed the stream, the 304 is confirming that
// listing — no second request needed.
func TestLatestRuntimeReusesTheListingThisSessionSaw(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const etag = `"seen-this-session"`
	base, served := serveStreamListing(t, "2.0.0", etag)
	t.Setenv("CIX_UPDATE_BASE_URL", base)

	u := newUpdater(bundle{})
	// A background check, exactly as onReady runs one: 200, and the ETag is now
	// cached both in memory and on disk. It lists twice — the app stream and the
	// server stream are separate clients over the same endpoint.
	u.check(true)
	if u.seenRuntime.Version != "2.0.0" {
		t.Fatalf("seenRuntime = %q, want 2.0.0", u.seenRuntime.Version)
	}
	afterCheck := *served

	rel, err := u.latestRuntime(context.Background())
	if err != nil {
		t.Fatalf("latestRuntime: %v", err)
	}
	if rel.Version != "2.0.0" {
		t.Errorf("version = %q, want 2.0.0", rel.Version)
	}
	if *served != afterCheck {
		t.Errorf("full listings served = %d, want %d — the 304 should have been answered from memory",
			*served, afterCheck)
	}
}
