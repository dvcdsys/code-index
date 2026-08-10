package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Self-update, end to end, against a fake releases API.
//
// This path had never been executed: no mac/v* release exists, so the app half
// of the updater has only ever seen an empty stream. Everything it does before
// the swap is reachable here — the version comparison that decides to offer,
// the download, the checksum, mounting the image and validating the signature —
// and the swap script itself is driven with a real process to wait for and a
// stub `open` on PATH.

// fakeRelease builds a DMG containing a minimal signed cix.app and serves it,
// with checksums.txt, from a fake GitHub releases API. Returns the base URL.
func fakeRelease(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()

	// A bundle, not a stub directory: stageFromDMG runs `codesign --verify
	// --strict` on what it copies out, and that is a check worth exercising
	// rather than working around.
	app := filepath.Join(dir, "payload", "cix.app")
	macOS := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macOS, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.dvcdsys.cix.test</string>
<key>CFBundleName</key><string>cix</string>
<key>CFBundleExecutable</key><string>cix-launcher</string>
<key>CFBundleShortVersionString</key><string>%s</string>
</dict></plist>`, version)
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	// Any Mach-O will do; the point is that a signature seals it. `true` is
	// present on every macOS and is tiny.
	if out, err := exec.Command("cp", "/usr/bin/true", filepath.Join(macOS, "cix-launcher")).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v: %s", err, out)
	}
	if out, err := exec.Command("codesign", "--force", "--sign", "-", app).CombinedOutput(); err != nil {
		t.Skipf("codesign unavailable in this environment: %v: %s", err, out)
	}

	dmgName := "cix-" + version + "-arm64.dmg"
	dmgPath := filepath.Join(dir, dmgName)
	if out, err := exec.Command("hdiutil", "create", "-quiet", "-srcfolder",
		filepath.Join(dir, "payload"), "-volname", "cix", "-format", "UDZO", dmgPath).CombinedOutput(); err != nil {
		t.Skipf("hdiutil unavailable in this environment: %v: %s", err, out)
	}

	sums, err := exec.Command("shasum", "-a", "256", dmgPath).Output()
	if err != nil {
		t.Fatal(err)
	}
	// The workflow writes "./name.dmg"; checksumFor matches on the base name,
	// so keep the awkward shape rather than a tidied one.
	sumLine := strings.Fields(string(sums))[0] + "  ./" + dmgName + "\n"
	sumsPath := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(sumsPath, []byte(sumLine), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/dvcdsys/code-index/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"fake-etag"`)
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name": "mac/v" + version,
			"html_url": "https://example.invalid",
			"assets": []map[string]any{
				{"name": dmgName, "browser_download_url": base + "/dl/" + dmgName},
				{"name": "checksums.txt", "browser_download_url": base + "/dl/checksums.txt"},
			},
		}})
	})
	mux.Handle("/dl/", http.StripPrefix("/dl/", http.FileServer(http.Dir(dir))))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	return srv.URL
}

func TestSelfUpdateOffersAndStages(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // prefs and the update cache live under it

	base := fakeRelease(t, "9.9.9")
	t.Setenv("CIX_UPDATE_BASE_URL", base)

	// The installed app, in a directory this test owns — the staged copy lands
	// beside it, which is what the real swap needs.
	apps := filepath.Join(home, "Applications")
	root := filepath.Join(apps, "cix.app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	// A stamped build: "dev" deliberately refuses to update itself, and that
	// refusal is why nothing was ever offered on a locally built app.
	restore := version
	version = "0.1.0"
	t.Cleanup(func() { version = restore })

	u := newUpdater(bundle{Root: root})
	av := u.check(true)
	if av.App.Version != "9.9.9" {
		t.Fatalf("check() offered app %q, want 9.9.9", av.App.Version)
	}
	// Only the mac stream is published in the fake API, so the runtime half
	// must stay quiet rather than treating "no releases" as an update.
	if av.Runtime.Version != "" {
		t.Errorf("check() offered runtime %q from a stream with no releases", av.Runtime.Version)
	}

	staged, err := u.stageUpdate(av.App, func(string) {})
	if err != nil {
		t.Fatalf("stageUpdate: %v", err)
	}
	if want := filepath.Join(apps, ".cix.app.new"); staged != want {
		t.Errorf("staged at %q, want %q", staged, want)
	}
	// Validated, not merely copied: the signature survived the DMG round trip.
	if out, err := exec.Command("codesign", "--verify", "--strict", staged).CombinedOutput(); err != nil {
		t.Errorf("staged bundle fails verification: %v: %s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(staged, "Contents", "Info.plist")); err != nil {
		t.Error(err)
	} else if !strings.Contains(string(b), "<string>9.9.9</string>") {
		t.Error("the staged bundle is not the version that was offered")
	}

	// A dev build must not be offered the app half, whatever is published.
	version = "dev"
	if got := u.check(true); got.App.Version != "" {
		t.Errorf("a development build was offered app %q", got.App.Version)
	}
}

// A corrupted download must be discarded rather than installed: with no
// Developer ID signature, this checksum is the whole integrity story.
func TestSelfUpdateRejectsATamperedImage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	base := fakeRelease(t, "9.9.9")
	t.Setenv("CIX_UPDATE_BASE_URL", base)

	apps := filepath.Join(home, "Applications")
	root := filepath.Join(apps, "cix.app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	restore := version
	version = "0.1.0"
	t.Cleanup(func() { version = restore })

	u := newUpdater(bundle{Root: root})
	av := u.check(true)

	// Point the DMG asset at the checksums file: the bytes fetched are then not
	// the bytes the release lists, which is exactly what a truncated or swapped
	// download looks like.
	for i, a := range av.App.Assets {
		if strings.HasSuffix(a.Name, ".dmg") {
			av.App.Assets[i].URL = base + "/dl/checksums.txt"
		}
	}

	staged, err := u.stageUpdate(av.App, func(string) {})
	if err == nil {
		t.Fatal("stageUpdate accepted an image that does not match its checksum")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("error = %v, want it to name the checksum", err)
	}
	if staged != "" {
		t.Errorf("staged = %q, want nothing staged", staged)
	}
	if _, err := os.Stat(filepath.Join(apps, ".cix.app.new")); !os.IsNotExist(err) {
		t.Error("a rejected update left a staged bundle behind")
	}
}

// swap.sh is the one part that runs after the app is gone, so its failure modes
// are the ones nobody is around to see.
func TestSwapScript(t *testing.T) {
	dir := t.TempDir()

	// Stub `open`, so the successful path can be exercised without handing a
	// fake bundle to LaunchServices.
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	openLog := filepath.Join(dir, "open.log")
	if err := os.WriteFile(filepath.Join(bin, "open"),
		[]byte("#!/bin/bash\necho \"$@\" >> "+openLog+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	script := filepath.Join(dir, "swap.sh")
	if err := os.WriteFile(script, swapScript, 0o755); err != nil {
		t.Fatal(err)
	}

	newBundle := func(t *testing.T, path, marker string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "marker"), []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("replaces the bundle once the launcher exits", func(t *testing.T) {
		live := filepath.Join(dir, "live", "cix.app")
		staged := filepath.Join(dir, "live", ".cix.app.new")
		newBundle(t, live, "old")
		newBundle(t, staged, "new")

		// A real process to wait for, which exits shortly after the script
		// starts — the sequence the update actually produces.
		victim := exec.Command("/bin/sleep", "1")
		if err := victim.Start(); err != nil {
			t.Fatal(err)
		}
		// Reaped concurrently, and that detail is not incidental: an exited
		// child whose parent has not waited on it keeps its pid in the process
		// table, where `kill -0` still succeeds — so an unreaped victim makes
		// swap.sh wait out its full patience and abort. In the real sequence
		// the launcher is a child of launchd, which reaps it immediately.
		reaped := make(chan struct{})
		go func() { _ = victim.Wait(); close(reaped) }()

		out, err := exec.Command("/bin/bash", script, fmt.Sprint(victim.Process.Pid),
			live, staged, filepath.Join(dir, "swap.log")).CombinedOutput()
		<-reaped
		if err != nil {
			t.Fatalf("swap.sh: %v: %s", err, out)
		}

		if b, err := os.ReadFile(filepath.Join(live, "marker")); err != nil || string(b) != "new" {
			t.Errorf("live bundle = %q (%v), want the staged one", b, err)
		}
		if _, err := os.Stat(staged); !os.IsNotExist(err) {
			t.Error("the staged bundle was left behind")
		}
		// Nothing half-updated: the moved-aside copy is cleaned up.
		if _, err := os.Stat(live + ".old"); !os.IsNotExist(err) {
			t.Error("the previous bundle was left at .old")
		}
		if b, err := os.ReadFile(openLog); err != nil || !strings.Contains(string(b), live) {
			t.Errorf("swap.sh did not reopen the app: %q (%v)", b, err)
		}
	})

	t.Run("refuses to swap under a live launcher", func(t *testing.T) {
		if testing.Short() {
			// The guard is a 60s timeout, and there is no way to observe it
			// firing without spending the 60s.
			t.Skip("takes a minute: swap.sh's patience is the thing under test")
		}
		live := filepath.Join(dir, "alive", "cix.app")
		staged := filepath.Join(dir, "alive", ".cix.app.new")
		newBundle(t, live, "old")
		newBundle(t, staged, "new")

		// Outlives the script's 60s patience, so the guard is what ends it.
		victim := exec.Command("/bin/sleep", "120")
		if err := victim.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = victim.Process.Kill()
			_ = victim.Wait()
		}()

		done := make(chan error, 1)
		cmd := exec.Command("/bin/bash", script, fmt.Sprint(victim.Process.Pid),
			live, staged, filepath.Join(dir, "swap-alive.log"))
		go func() { done <- cmd.Run() }()

		select {
		case err := <-done:
			if err == nil {
				t.Error("swap.sh replaced a bundle under a running launcher")
			}
		case <-time.After(75 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("swap.sh did not give up waiting for a launcher that never exits")
		}

		if b, err := os.ReadFile(filepath.Join(live, "marker")); err != nil || string(b) != "old" {
			t.Errorf("live bundle = %q (%v), want it untouched", b, err)
		}
	})
}
