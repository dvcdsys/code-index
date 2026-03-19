package discovery

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// helper: create a file with content
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// helper: extract RelPath list from discovered files, sorted
func relPaths(files []DiscoveredFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.RelPath)
	}
	sort.Strings(out)
	return out
}

func TestDiscover_RootGitignore(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\nsecret.txt\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "app.log"), "some log\n")
	writeFile(t, filepath.Join(root, "secret.txt"), "password\n")
	writeFile(t, filepath.Join(root, "readme.txt"), "hello\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	// main.go and readme.txt should be discovered; app.log and secret.txt ignored
	expected := []string{"main.go", "readme.txt"}
	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

func TestDiscover_NestedGitignore(t *testing.T) {
	root := t.TempDir()

	// Root .gitignore ignores *.log
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "root.log"), "root log\n")

	// Subproject has its own .gitignore that ignores *.tmp and generated/
	sub := filepath.Join(root, "subproject")
	writeFile(t, filepath.Join(sub, ".gitignore"), "*.tmp\ngenerated/\n")
	writeFile(t, filepath.Join(sub, "app.go"), "package sub\n")
	writeFile(t, filepath.Join(sub, "data.tmp"), "temp data\n")
	writeFile(t, filepath.Join(sub, "sub.log"), "sub log\n") // ignored by root .gitignore
	writeFile(t, filepath.Join(sub, "generated", "output.go"), "package gen\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	// Expected: main.go, subproject/app.go
	// Ignored: root.log (root *.log), subproject/sub.log (root *.log),
	//          subproject/data.tmp (sub *.tmp), subproject/generated/output.go (sub generated/)
	expected := []string{"main.go", filepath.Join("subproject", "app.go")}
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

func TestDiscover_NestedGitignoreDoesNotAffectSibling(t *testing.T) {
	root := t.TempDir()

	// dirA has .gitignore ignoring *.txt
	dirA := filepath.Join(root, "a")
	writeFile(t, filepath.Join(dirA, ".gitignore"), "*.txt\n")
	writeFile(t, filepath.Join(dirA, "code.go"), "package a\n")
	writeFile(t, filepath.Join(dirA, "notes.txt"), "notes\n")

	// dirB has NO .gitignore — .txt should be allowed here
	dirB := filepath.Join(root, "b")
	writeFile(t, filepath.Join(dirB, "code.go"), "package b\n")
	writeFile(t, filepath.Join(dirB, "notes.txt"), "notes\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	// a/notes.txt should be ignored, b/notes.txt should NOT
	expected := []string{
		filepath.Join("a", "code.go"),
		filepath.Join("b", "code.go"),
		filepath.Join("b", "notes.txt"),
	}
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

func TestDiscover_DeeplyNestedGitignore(t *testing.T) {
	root := t.TempDir()

	// root/.gitignore ignores *.log
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")

	// root/a/b/.gitignore ignores *.dat
	deep := filepath.Join(root, "a", "b")
	writeFile(t, filepath.Join(deep, ".gitignore"), "*.dat\n")
	writeFile(t, filepath.Join(deep, "code.go"), "package b\n")
	writeFile(t, filepath.Join(deep, "data.dat"), "binary\n")
	writeFile(t, filepath.Join(deep, "err.log"), "error\n")

	// root/a should not be affected by a/b/.gitignore
	writeFile(t, filepath.Join(root, "a", "info.dat"), "info\n")
	writeFile(t, filepath.Join(root, "a", "code.go"), "package a\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	// a/info.dat is allowed (a/b/.gitignore should not affect parent)
	// a/b/data.dat is ignored (a/b/.gitignore)
	// a/b/err.log is ignored (root .gitignore)
	expected := []string{
		filepath.Join("a", "code.go"),
		filepath.Join("a", "info.dat"),
		filepath.Join("a", "b", "code.go"),
	}
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

func TestDiscover_NoGitignore(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "data.txt"), "data\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	expected := []string{"data.txt", "main.go"}

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

func TestDiscover_GitignoreDirectoryPattern(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, ".gitignore"), "logs/\ntmp/\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "logs", "app.log"), "log\n")
	writeFile(t, filepath.Join(root, "tmp", "cache.go"), "package tmp\n")
	writeFile(t, filepath.Join(root, "src", "app.go"), "package src\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	expected := []string{"main.go", filepath.Join("src", "app.go")}
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}
func TestDiscover_IgnoredDirOwnGitignoreDoesNotPreventSkip(t *testing.T) {
	root := t.TempDir()

	// Root .gitignore ignores the "generated" directory
	writeFile(t, filepath.Join(root, ".gitignore"), "generated/\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")

	// generated/ has its own .gitignore with a negation pattern —
	// it should NOT prevent the directory from being skipped by root .gitignore
	gen := filepath.Join(root, "generated")
	writeFile(t, filepath.Join(gen, ".gitignore"), "!*.go\n")
	writeFile(t, filepath.Join(gen, "output.go"), "package gen\n")
	writeFile(t, filepath.Join(gen, "output.txt"), "text\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	// generated/ is ignored by root — nothing inside should appear
	expected := []string{"main.go"}

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

// ---------------------------------------------------------------------------
// .cixignore tests
// ---------------------------------------------------------------------------

func TestDiscover_RootCixignore(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, ".cixignore"), "vendor-ext/\n*.generated.go\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "handler.generated.go"), "package main\n")
	writeFile(t, filepath.Join(root, "vendor-ext", "lib.go"), "package lib\n")
	writeFile(t, filepath.Join(root, "src", "app.go"), "package src\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	expected := []string{"main.go", filepath.Join("src", "app.go")}
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

func TestDiscover_CixignoreAndGitignoreMerged(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, ".cixignore"), "*.tmp\n")

	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "app.log"), "log\n")
	writeFile(t, filepath.Join(root, "cache.tmp"), "temp\n")
	writeFile(t, filepath.Join(root, "readme.txt"), "hello\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	expected := []string{"main.go", "readme.txt"}
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

func TestDiscover_CixignoreDirectoryPattern(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, ".cixignore"), "submodules/\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "submodules", "vendor", "lib.go"), "package lib\n")
	writeFile(t, filepath.Join(root, "submodules", "contracts", "token.sol"), "contract\n")
	writeFile(t, filepath.Join(root, "src", "app.go"), "package src\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	expected := []string{"main.go", filepath.Join("src", "app.go")}
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

func TestDiscover_NestedCixignore(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, ".cixignore"), "*.dat\n")

	sub := filepath.Join(root, "sub")
	writeFile(t, filepath.Join(sub, ".cixignore"), "*.tmp\n")
	writeFile(t, filepath.Join(sub, "code.go"), "package sub\n")
	writeFile(t, filepath.Join(sub, "cache.tmp"), "temp\n")
	writeFile(t, filepath.Join(sub, "data.dat"), "data\n")

	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "info.dat"), "info\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	expected := []string{"main.go", filepath.Join("sub", "code.go")}
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

func TestDiscover_CixignoreFileNotIndexed(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, ".cixignore"), "*.log\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range files {
		if f.RelPath == ".cixignore" {
			t.Error(".cixignore file itself should not be indexed")
		}
	}
}

// ---------------------------------------------------------------------------
// .cixconfig.yaml + submodules tests
// ---------------------------------------------------------------------------

func TestDiscover_CixconfigSubmodulesIgnored(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, ".cixconfig.yaml"), "ignore:\n  submodules: true\n")
	writeFile(t, filepath.Join(root, ".gitmodules"), `[submodule "libs/vendor"]
	path = libs/vendor
	url = https://example.com/vendor.git
[submodule "third_party/contracts"]
	path = third_party/contracts
	url = https://example.com/contracts.git
`)
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "libs", "vendor", "lib.go"), "package vendor\n")
	writeFile(t, filepath.Join(root, "libs", "vendor", "deep", "util.go"), "package deep\n")
	writeFile(t, filepath.Join(root, "third_party", "contracts", "token.sol"), "contract\n")
	writeFile(t, filepath.Join(root, "src", "app.go"), "package src\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	expected := []string{"main.go", filepath.Join("src", "app.go")}
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

func TestDiscover_CixconfigSubmodulesFalse_NotIgnored(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, ".cixconfig.yaml"), "ignore:\n  submodules: false\n")
	writeFile(t, filepath.Join(root, ".gitmodules"), `[submodule "libs/external"]
	path = libs/external
	url = https://example.com/external.git
`)
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "libs", "external", "lib.go"), "package ext\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	// Both files should be discovered since submodules: false
	expected := []string{filepath.Join("libs", "external", "lib.go"), "main.go"}
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

func TestDiscover_CixconfigNoGitmodules(t *testing.T) {
	root := t.TempDir()

	// submodules: true but no .gitmodules file — should not break
	writeFile(t, filepath.Join(root, ".cixconfig.yaml"), "ignore:\n  submodules: true\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	if len(got) != 1 || got[0] != "main.go" {
		t.Fatalf("expected [main.go], got %v", got)
	}
}

func TestDiscover_CixconfigWithCixignoreCombined(t *testing.T) {
	root := t.TempDir()

	// .cixconfig.yaml excludes submodules, .cixignore excludes *.tmp
	writeFile(t, filepath.Join(root, ".cixconfig.yaml"), "ignore:\n  submodules: true\n")
	writeFile(t, filepath.Join(root, ".gitmodules"), `[submodule "vendor"]
	path = vendor
	url = https://example.com/vendor.git
`)
	writeFile(t, filepath.Join(root, ".cixignore"), "*.tmp\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "cache.tmp"), "temp\n")
	writeFile(t, filepath.Join(root, "src", "lib.go"), "package src\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	expected := []string{"main.go", filepath.Join("src", "lib.go")}
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}

func TestDiscover_OnlyCixignoreNoGitignore(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, ".cixignore"), "generated/\n*.bak\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "config.bak"), "old config\n")
	writeFile(t, filepath.Join(root, "generated", "api.go"), "package gen\n")

	files, err := Discover(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(files)
	expected := []string{"main.go"}

	if len(got) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %q at position %d, got %q", expected[i], i, got[i])
		}
	}
}
