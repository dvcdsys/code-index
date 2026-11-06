package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestExtractZipRoundTrip(t *testing.T) {
	dir := t.TempDir()
	z := filepath.Join(dir, "a.zip")
	writeZip(t, z, map[string]string{
		"cix-runtime-1.2.3/cix-server.exe":         "x",
		"cix-runtime-1.2.3/llama/llama-server.exe": "y",
	})
	out := filepath.Join(dir, "out")
	if err := extractZip(z, out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "cix-runtime-1.2.3", "llama", "llama-server.exe"))
	if err != nil || string(b) != "y" {
		t.Fatalf("got %q, %v", b, err)
	}
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	for _, name := range []string{"../evil.exe", "a/../../evil.exe", `a\..\evil.exe`, "C:/evil.exe", "/abs.exe"} {
		dir := t.TempDir()
		z := filepath.Join(dir, "a.zip")
		writeZip(t, z, map[string]string{name: "x"})
		if err := extractZip(z, filepath.Join(dir, "out")); err == nil {
			t.Errorf("extractZip accepted %q", name)
		}
	}
}

func TestJunctionSwap(t *testing.T) {
	root := t.TempDir()
	setHome(t, root)
	runtimeDir := filepath.Join(root, ".cix", "runtime")
	for _, v := range []string{"0.1.0", "0.2.0"} {
		if err := os.MkdirAll(filepath.Join(runtimeDir, v), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(runtimeDir, v, "cix-server.exe"), []byte(v), 0o755)
	}

	prev, err := activateRuntime("0.1.0")
	if err != nil || prev != "" {
		t.Fatalf("first activate: prev=%q err=%v", prev, err)
	}
	if got := currentRuntimeVersion(); got != "0.1.0" {
		t.Fatalf("current = %q, want 0.1.0", got)
	}
	if !runtimeReady() {
		t.Fatal("runtime should be ready through the junction")
	}
	// The launcher reads through the resolved path, never through the junction:
	// Redirection Guard makes the junction untraversable for processes that
	// inherited the policy from an installer.
	if live, _ := runtimeLiveDir(); filepath.Base(live) != "0.1.0" || filepath.Base(live) == runtimeLinkName {
		t.Fatalf("runtimeLiveDir = %q, want the versioned directory", live)
	}

	prev, err = activateRuntime("0.2.0")
	if err != nil || prev != "0.1.0" {
		t.Fatalf("second activate: prev=%q err=%v", prev, err)
	}
	b, _ := os.ReadFile(filepath.Join(runtimeDir, "current", "cix-server.exe"))
	if string(b) != "0.2.0" {
		t.Fatalf("current resolves to %q, want 0.2.0", b)
	}

	// Pruning a version must not follow the junction into the one it points at.
	pruneRuntimes("0.2.0")
	if _, err := os.Stat(filepath.Join(runtimeDir, "0.2.0", "cix-server.exe")); err != nil {
		t.Fatalf("active version was damaged: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "0.1.0")); err == nil {
		t.Fatal("0.1.0 should have been pruned")
	}
}

func TestBuildTaskXML(t *testing.T) {
	x, err := buildTaskXML(`C:\Users\a b\.cix\runtime\current\cix-server.exe`, `C:\Users\a b\.cix\server.env`,
		`C:\Users\a b\.cix\logs`, `C:\Users\a b\.cix`, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{managedByMarker, "<LogonTrigger>", "<Hidden>true</Hidden>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>", "IgnoreNew", `-env-file "C:\Users\a b\.cix\server.env"`} {
		if !strings.Contains(x, want) {
			t.Errorf("task XML lacks %q", want)
		}
	}
	off, _ := buildTaskXML("s", "e", "l", "w", false)
	if strings.Contains(off, "<LogonTrigger>") {
		t.Error("autostart off must not register a logon trigger")
	}
}

func TestFormatUptime(t *testing.T) {
	cases := map[string]string{"7m": formatUptimeMin(7), "4h 12m": formatUptimeMin(252), "2d 3h": formatUptimeMin(51 * 60)}
	for want, got := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
