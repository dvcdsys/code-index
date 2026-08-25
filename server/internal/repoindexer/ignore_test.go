package repoindexer

import (
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// filterFor builds the filter IndexDir would build for a tree, so these tests
// exercise collection and matching together — the pair is what callers see.
func filterFor(t *testing.T, root string) FileFilter {
	t.Helper()
	f := DefaultFilter()
	ps, err := collectIgnorePatterns(root, f.ExcludeDirs)
	if err != nil {
		t.Fatalf("collectIgnorePatterns: %v", err)
	}
	if len(ps) > 0 {
		f.ignore = gitignore.NewMatcher(ps)
	}
	return f
}

// ignoreCase is one (path, isDir) → ignored assertion.
type ignoreCase struct {
	path  string
	isDir bool
	want  bool
}

func runIgnoreCases(t *testing.T, f FileFilter, cases []ignoreCase) {
	t.Helper()
	for _, c := range cases {
		kind := "file"
		if c.isDir {
			kind = "dir"
		}
		if got := f.ignored(c.path, c.isDir); got != c.want {
			t.Errorf("ignored(%q, %s) = %v, want %v", c.path, kind, got, c.want)
		}
	}
}

func TestCollectIgnorePatterns_EmptyTree(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"main.go": "package main\n"})

	ps, err := collectIgnorePatterns(root, DefaultFilter().ExcludeDirs)
	if err != nil {
		t.Fatalf("collectIgnorePatterns: %v", err)
	}
	if len(ps) != 0 {
		t.Fatalf("expected no patterns in a tree with no ignore files, got %d", len(ps))
	}
	// The nil matcher must be inert rather than a panic — DefaultFilter()
	// leaves it nil and is used on every hot path.
	f := DefaultFilter()
	if f.ignored("anything.go", false) || f.ignoredWithParents("a/b.go") {
		t.Fatal("a filter with no ignore rules must ignore nothing")
	}
}

// A clone job that failed before checkout leaves no directory at all; the
// index walk already tolerates that, so collection must too.
func TestCollectIgnorePatterns_MissingRoot(t *testing.T) {
	ps, err := collectIgnorePatterns(filepath.Join(t.TempDir(), "never-cloned"), nil)
	if err != nil {
		t.Fatalf("missing root should not be an error, got %v", err)
	}
	if ps != nil {
		t.Fatalf("expected nil patterns, got %d", len(ps))
	}
}

func TestIgnored_RootPatterns(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".cixignore": "*.log\nbuild/\n/onlyroot.go\ndocs/*.md\n",
	})
	f := filterFor(t, root)

	runIgnoreCases(t, f, []ignoreCase{
		{"a.log", false, true},
		{"sub/deep/a.log", false, true}, // unanchored globs match at any depth
		{"build", true, true},           // dirOnly pattern prunes the directory
		{"build/x.go", false, true},     // ...and everything under it
		{"onlyroot.go", false, true},    // leading slash anchors to the root
		{"sub/onlyroot.go", false, false},
		{"docs/readme.md", false, true},
		{"docs/deep/readme.md", false, false}, // "*" does not cross a separator
		{"keep.go", false, false},
	})
}

// A "build/" pattern must prune the directory but leave a plain FILE named
// "build" alone. This is the case that breaks if shouldSkipDir passes
// isDir=false: go-git only enforces dirOnly on the last path segment.
func TestIgnored_DirOnlyPattern(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{".cixignore": "build/\n"})
	f := filterFor(t, root)

	runIgnoreCases(t, f, []ignoreCase{
		{"build", true, true},
		{"build", false, false},
		{"build/x.go", false, true},
	})
}

func TestIgnored_NestedDomainScoping(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a/.gitignore": "x.go\n",
		"a/x.go":       "package a\n",
		"b/x.go":       "package b\n",
	})
	f := filterFor(t, root)

	runIgnoreCases(t, f, []ignoreCase{
		{"a/x.go", false, true},
		{"a/deep/x.go", false, true},
		{"b/x.go", false, false}, // a sibling's rules never reach here
	})
}

func TestIgnored_GitignoreAndCixignoreMerged(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore": "*.log\n",
		".cixignore": "vendor-ext/\n",
	})
	f := filterFor(t, root)

	runIgnoreCases(t, f, []ignoreCase{
		{"a.log", false, true},
		{"vendor-ext/x.go", false, true},
		{"keep.go", false, false},
	})
}

// .cixignore is loaded after .gitignore, and go-git resolves to the LAST
// matching pattern, so a cix-specific re-inclusion wins. The CLI cannot do
// this — it returns on the first matcher that hits.
func TestIgnored_CixignoreOverridesGitignore(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore": "*.log\n",
		".cixignore": "!keep.log\n",
	})
	f := filterFor(t, root)

	runIgnoreCases(t, f, []ignoreCase{
		{"keep.log", false, false},
		{"other.log", false, true},
	})
}

// Re-inclusion follows git's rule that a "!" cannot rescue a file whose parent
// directory is excluded — verified against `git check-ignore` on both shapes
// below, because getting this backwards would quietly drop real source.
func TestIgnored_NegationNeedsTheParentBack(t *testing.T) {
	// Naive shape: "docs/*" excludes docs/api, so the negation never applies.
	// git check-ignore reports docs/api/keep.md as IGNORED here.
	naive := t.TempDir()
	writeTree(t, naive, map[string]string{
		".cixignore": "docs/*\n!docs/api/keep.md\n",
	})
	f := filterFor(t, naive)

	if f.ignored("docs", true) {
		t.Error(`"docs/*" must not prune "docs" itself`)
	}
	if !f.ignored("docs/api", true) {
		t.Error(`"docs/*" must prune "docs/api" — that is what defeats the negation`)
	}
	if !f.ignoredWithParents("docs/api/keep.md") {
		t.Error("keep.md must stay excluded: its parent directory is excluded")
	}
	if !f.ignored("docs/other.md", false) {
		t.Error("docs/other.md must be excluded")
	}

	// Working shape: re-include the directory too, and the file comes back.
	// git check-ignore reports docs/api/keep.md as tracked here.
	fixed := t.TempDir()
	writeTree(t, fixed, map[string]string{
		".cixignore": "docs/*\n!docs/api/\n!docs/api/keep.md\n",
	})
	f = filterFor(t, fixed)

	if f.ignored("docs/api", true) {
		t.Error(`"!docs/api/" must bring the directory back`)
	}
	if f.ignoredWithParents("docs/api/keep.md") {
		t.Error("keep.md must be indexed once its parent is re-included")
	}
	if !f.ignored("docs/other.md", false) {
		t.Error("docs/other.md must still be excluded")
	}
}

// Git cannot re-include a file whose parent directory is excluded. The walk
// enforces that by pruning; ignoredWithParents enforces it for the
// incremental driver, which has no traversal to prune.
func TestIgnored_NegationUnderExcludedParent(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".cixignore": "docs/\n!docs/api/keep.md\n",
	})
	f := filterFor(t, root)

	if !f.ignored("docs", true) {
		t.Error("docs/ must prune the docs directory")
	}
	if !f.ignoredWithParents("docs/api/keep.md") {
		t.Error("a file under an excluded directory must stay excluded")
	}
	// Both drivers must agree, or reconcile deletes what incremental adds.
	if f.ignored("docs", true) != f.ignoredWithParents("docs/api/keep.md") {
		t.Error("walk and incremental drivers disagree about the same tree")
	}
}

func TestIgnored_DeeperFileOverridesShallower(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".cixignore":     "*.gen.go\n",
		"sub/.cixignore": "!*.gen.go\n",
	})
	f := filterFor(t, root)

	runIgnoreCases(t, f, []ignoreCase{
		{"a.gen.go", false, true},
		{"sub/a.gen.go", false, false},
	})
}

// Comments, blank lines and CRLF endings all have to survive the trip into
// gitignore.ParsePattern, which handles none of them itself.
func TestCollectIgnorePatterns_CommentsBlanksAndCRLF(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".cixignore": "# a comment\r\n\r\n*.log\r\n   \n",
	})
	f := filterFor(t, root)

	runIgnoreCases(t, f, []ignoreCase{
		{"a.log", false, true},
		{"keep.go", false, false},
		// A comment parsed as a pattern would ignore a file named after it.
		{"a comment", false, false},
	})
}

// An ignore file inside an already-ignored directory must not be read: a "!*"
// in there would resurrect the subtree its parent just excluded.
func TestCollectIgnorePatterns_PrunesIgnoredDirs(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".cixignore":        "secret/\n",
		"secret/.cixignore": "!*\n",
		"secret/x.go":       "package secret\n",
	})
	f := filterFor(t, root)

	if !f.ignored("secret", true) {
		t.Error("secret/ must stay pruned")
	}
	if !f.ignoredWithParents("secret/x.go") {
		t.Error("a nested !* must not resurrect an excluded subtree")
	}
}

func TestCollectIgnorePatterns_PrunesExcludeDirs(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".git/.gitignore":         "*.go\n",
		"node_modules/.cixignore": "*.go\n",
		"main.go":                 "package main\n",
	})

	ps, err := collectIgnorePatterns(root, DefaultFilter().ExcludeDirs)
	if err != nil {
		t.Fatalf("collectIgnorePatterns: %v", err)
	}
	if len(ps) != 0 {
		t.Fatalf("ignore files under excluded dirs must not be read, got %d patterns", len(ps))
	}
}

// strings.Split("", "/") is [""] and filepath.Match("*", "") is true, so a
// bare "*" would prune the repo root and index nothing at all.
func TestIgnored_EmptyAndDotPathsNeverMatch(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{".cixignore": "*\n"})
	f := filterFor(t, root)

	for _, p := range []string{"", ".", "/", "./"} {
		if f.ignored(p, true) {
			t.Errorf("ignored(%q, dir) must be false — that is the repo root", p)
		}
		if f.ignoredWithParents(p) {
			t.Errorf("ignoredWithParents(%q) must be false", p)
		}
	}
}

func TestIsIgnoreFile(t *testing.T) {
	cases := map[string]bool{
		".gitignore":          true,
		".cixignore":          true,
		"sub/deep/.gitignore": true,
		"sub/.cixignore":      true,
		".gitignore.bak":      false,
		"gitignore":           false,
		"sub/main.go":         false,
		"":                    false,
	}
	for in, want := range cases {
		if got := IsIgnoreFile(in); got != want {
			t.Errorf("IsIgnoreFile(%q) = %v, want %v", in, got, want)
		}
	}
}
