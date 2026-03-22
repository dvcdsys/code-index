package fileutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsBinary_TextFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0644)

	if IsBinary(path) {
		t.Error("expected Go source file to be non-binary")
	}
}

func TestIsBinary_MarkdownFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	os.WriteFile(path, []byte("# Title\n\nSome text.\n"), 0644)

	if IsBinary(path) {
		t.Error("expected Markdown file to be non-binary")
	}
}

func TestIsBinary_JSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"key": "value"}`), 0644)

	if IsBinary(path) {
		t.Error("expected JSON file to be non-binary")
	}
}

func TestIsBinary_PNGFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "image.png")
	// PNG magic bytes
	os.WriteFile(path, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}, 0644)

	if !IsBinary(path) {
		t.Error("expected PNG file to be binary")
	}
}

func TestIsBinary_ELFBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "program")
	// ELF magic bytes
	os.WriteFile(path, []byte{0x7F, 0x45, 0x4C, 0x46, 0x02, 0x01, 0x01, 0x00, 0x00, 0x00}, 0644)

	if !IsBinary(path) {
		t.Error("expected ELF binary to be binary")
	}
}

func TestIsBinary_ExtensionlessBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mybinary")
	// Mach-O magic bytes (64-bit)
	os.WriteFile(path, []byte{0xCF, 0xFA, 0xED, 0xFE, 0x07, 0x00, 0x00, 0x01, 0x03, 0x00}, 0644)

	if !IsBinary(path) {
		t.Error("expected extensionless Mach-O binary to be detected as binary")
	}
}

func TestIsBinary_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty")
	os.WriteFile(path, []byte{}, 0644)

	if IsBinary(path) {
		t.Error("expected empty file to be non-binary")
	}
}

func TestIsBinary_NonExistentFile(t *testing.T) {
	if IsBinary("/no/such/file") {
		t.Error("expected non-existent file to be non-binary")
	}
}

func TestIsBinary_ZIPFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive")
	// ZIP magic bytes
	os.WriteFile(path, []byte{0x50, 0x4B, 0x03, 0x04, 0x00, 0x00, 0x00, 0x00}, 0644)

	if !IsBinary(path) {
		t.Error("expected ZIP archive to be binary")
	}
}

func TestIsBinary_GZIPFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	// GZIP magic bytes
	os.WriteFile(path, []byte{0x1F, 0x8B, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}, 0644)

	if !IsBinary(path) {
		t.Error("expected GZIP file to be binary")
	}
}