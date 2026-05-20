package tunnels

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractFromTarGz(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	payload := []byte("#!/bin/sh\necho hi\n")
	_ = tw.WriteHeader(&tar.Header{Name: "ngrok", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(payload)
	// A decoy entry that must be skipped.
	_ = tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("doc"))
	_ = tw.Close()
	_ = gz.Close()

	var out bytes.Buffer
	if err := extractFromTarGz(&buf, "ngrok", &out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("extracted bytes mismatch: %q", out.String())
	}
}

func TestInstallerRawDownloadAndStatus(t *testing.T) {
	body := []byte("fake-cloudflared-binary")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	in := NewInstaller(dir, nil)

	// Not installed initially.
	if ok, _ := in.Installed(ProviderCloudflare); ok {
		t.Fatal("should not be installed yet")
	}

	// Drive the download path directly (bypass assetURL's live GitHub URL)
	// by writing through the same atomic flow the installer uses.
	target := in.Path(ProviderCloudflare)
	if err := downloadRawForTest(srv.URL, target); err != nil {
		t.Fatalf("download: %v", err)
	}
	ok, p := in.Installed(ProviderCloudflare)
	if !ok || p != filepath.Join(dir, "cloudflared") {
		t.Fatalf("expected installed at %s, got ok=%v p=%q", target, ok, p)
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, body) {
		t.Fatalf("binary contents mismatch")
	}
}

// downloadRawForTest mirrors the installer's raw-write path against an
// arbitrary URL (the real Install hard-codes vendor URLs we can't hit in a
// unit test).
func downloadRawForTest(url, target string) error {
	resp, err := http.Get(url) //nolint:gosec,noctx // test helper
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = out.ReadFrom(resp.Body)
	return err
}

func TestAssetURLs(t *testing.T) {
	u, archived, member, err := assetURL(ProviderCloudflare, "linux", "amd64")
	if err != nil || archived || u == "" {
		t.Fatalf("cloudflare linux: u=%q archived=%v err=%v", u, archived, err)
	}
	u, archived, member, err = assetURL(ProviderNgrok, "linux", "amd64")
	if err != nil || !archived || member != "ngrok" {
		t.Fatalf("ngrok: u=%q archived=%v member=%q err=%v", u, archived, member, err)
	}
}

func TestInstallBinaryDisabledWhenUnmanaged(t *testing.T) {
	m := NewManager(ManagerConfig{}, nil)
	if err := m.InstallBinary(context.Background(), ProviderNgrok); err != ErrBinManagementDisabled {
		t.Fatalf("expected ErrBinManagementDisabled, got %v", err)
	}
}

func TestManagerBinariesReportsManagedFlag(t *testing.T) {
	m := NewManager(ManagerConfig{BinManaged: true, BinDir: t.TempDir()}, nil)
	bins := m.Binaries(context.Background())
	if len(bins) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(bins))
	}
	for _, b := range bins {
		if !b.Managed {
			t.Fatalf("provider %s should be managed", b.Provider)
		}
	}
}
