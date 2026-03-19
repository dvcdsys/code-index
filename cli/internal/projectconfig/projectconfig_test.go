package projectconfig

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

func TestLoad_SubmodulesTrue(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".cixconfig.yaml"), "ignore:\n  submodules: true\n")

	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Ignore.Submodules {
		t.Error("expected Ignore.Submodules to be true")
	}
}

func TestLoad_SubmodulesFalse(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".cixconfig.yaml"), "ignore:\n  submodules: false\n")

	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ignore.Submodules {
		t.Error("expected Ignore.Submodules to be false")
	}
}

func TestLoad_NoFile(t *testing.T) {
	root := t.TempDir()

	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ignore.Submodules {
		t.Error("expected default Ignore.Submodules to be false")
	}
}

func TestLoad_EmptyFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".cixconfig.yaml"), "")

	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ignore.Submodules {
		t.Error("expected default Ignore.Submodules to be false for empty file")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".cixconfig.yaml"), ":::invalid yaml{{{\n")

	_, err := Load(root)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

// ---------------------------------------------------------------------------
// SubmodulePaths
// ---------------------------------------------------------------------------

func TestSubmodulePaths_Standard(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitmodules"), `[submodule "api/schema/pf3-contract"]
	path = api/schema/pf3-contract
	url = https://github.com/Example/pf3-contract.git
[submodule "api/smart-contracts/pf3-smart-contracts"]
	path = api/smart-contracts/pf3-smart-contracts
	url = https://github.com/Example/pf3-smart-contracts.git
`)

	paths, err := SubmodulePaths(root)
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(paths)
	expected := []string{"api/schema/pf3-contract", "api/smart-contracts/pf3-smart-contracts"}

	if len(paths) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, paths)
	}
	for i := range expected {
		if paths[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, paths[i])
		}
	}
}

func TestSubmodulePaths_NoFile(t *testing.T) {
	root := t.TempDir()

	paths, err := SubmodulePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("expected no paths, got %v", paths)
	}
}

func TestSubmodulePaths_EmptyFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitmodules"), "")

	paths, err := SubmodulePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("expected no paths, got %v", paths)
	}
}

func TestSubmodulePaths_SingleSubmodule(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitmodules"), `[submodule "libs/vendor"]
	path = libs/vendor
	url = https://github.com/Example/vendor.git
`)

	paths, err := SubmodulePaths(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(paths) != 1 || paths[0] != "libs/vendor" {
		t.Errorf("expected [libs/vendor], got %v", paths)
	}
}

func TestSubmodulePaths_TabsAndSpaces(t *testing.T) {
	root := t.TempDir()
	// Mixing tabs and spaces around "path ="
	writeFile(t, filepath.Join(root, ".gitmodules"), "[submodule \"a\"]\n\tpath = sub/a\n[submodule \"b\"]\n  path=sub/b\n")

	paths, err := SubmodulePaths(root)
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(paths)
	expected := []string{"sub/a", "sub/b"}

	if len(paths) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, paths)
	}
	for i := range expected {
		if paths[i] != expected[i] {
			t.Errorf("expected %q, got %q", expected[i], paths[i])
		}
	}
}