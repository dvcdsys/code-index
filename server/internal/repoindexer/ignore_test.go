package repoindexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// filterFor builds the filter IndexDir would build for a tree, so these tests
// exercise collection and matching together — the pair is what callers see.
func filterFor(t *testing.T, root string) FileFilter {
	t.Helper()
	f := DefaultFilter()
	ps, err := collectIgnorePatterns(context.Background(), root, f.ExcludeDirs, nil)
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

	ps, err := collectIgnorePatterns(context.Background(), root, DefaultFilter().ExcludeDirs, nil)
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
	ps, err := collectIgnorePatterns(context.Background(), filepath.Join(t.TempDir(), "never-cloned"), nil, nil)
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

	ps, err := collectIgnorePatterns(context.Background(), root, DefaultFilter().ExcludeDirs, nil)
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

// The incremental driver reads only the ancestor chains of its change set, so
// the pattern set it builds must give the SAME answer as a full collection for
// every path it asks about — including when unrelated subtrees carry rules
// that would have been collected by the full walk.
func TestCollectIgnorePatternsFor_MatchesFullCollection(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".cixignore":      "*.log\n",
		"a/.gitignore":    "gen/\n",
		"a/gen/x.go":      "package gen\n",
		"a/b/.cixignore":  "*.tmp\n",
		"a/b/keep.go":     "package b\n",
		"a/b/scratch.tmp": "x\n",
		// An unrelated subtree whose rules must not change any answer below,
		// and which the restricted pass has no reason to read at all.
		"far/away/.cixignore": "!*.log\nkeep.go\n",
		"far/away/keep.go":    "package away\n",
	})

	targets := []string{"a/b/keep.go", "a/b/scratch.tmp", "a/gen/x.go", "top.log", "a/b/c/deep.log"}

	full := DefaultFilter()
	fps, err := collectIgnorePatterns(context.Background(), root, full.ExcludeDirs, nil)
	if err != nil {
		t.Fatalf("full collect: %v", err)
	}
	if len(fps) > 0 {
		full.ignore = gitignore.NewMatcher(fps)
	}

	restricted := DefaultFilter()
	rps, err := collectIgnorePatternsFor(context.Background(), root, restricted.ExcludeDirs, nil, targets)
	if err != nil {
		t.Fatalf("restricted collect: %v", err)
	}
	if len(rps) > 0 {
		restricted.ignore = gitignore.NewMatcher(rps)
	}

	if len(rps) >= len(fps) {
		t.Errorf("restricted collection read %d patterns vs %d for the full walk — it is not actually skipping anything", len(rps), len(fps))
	}

	for _, p := range targets {
		if got, want := restricted.ignoredWithParents(p), full.ignoredWithParents(p); got != want {
			t.Errorf("ignoredWithParents(%q): restricted=%v, full=%v", p, got, want)
		}
	}

	// Sanity-check that the fixture actually exercises the rules, otherwise
	// the equivalence above would be vacuously true.
	if !full.ignoredWithParents("a/b/scratch.tmp") {
		t.Error("fixture broken: a/b/.cixignore *.tmp should have matched")
	}
	if !full.ignoredWithParents("a/gen/x.go") {
		t.Error("fixture broken: a/.gitignore gen/ should have matched")
	}
	if full.ignoredWithParents("a/b/keep.go") {
		t.Error("fixture broken: a/b/keep.go should survive")
	}
}

// ancestorDirSet decides what the restricted pass descends into; an off-by-one
// there would silently drop a rule.
func TestAncestorDirSet(t *testing.T) {
	got := ancestorDirSet([]string{"a/b/c.go", "top.go"}, []string{"a/d/e/f.go"})
	want := map[string]bool{"a": true, "a/b": true, "a/d": true, "a/d/e": true}

	for k := range want {
		if !got[k] {
			t.Errorf("missing ancestor %q", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected ancestor %q", k)
		}
	}
	// The repo root is implicit — collection always starts there, and a "."
	// or "" key would be meaningless as a directory name to descend into.
	for _, k := range []string{".", "", "/"} {
		if got[k] {
			t.Errorf("root must not appear as a key, found %q", k)
		}
	}
}

// A ReadDir failure must not fail the job: a directory the collector cannot
// list is one the index walk cannot list either, so its subtree is not going
// to be indexed and missing its rules cannot cause over-indexing.
func TestCollectIgnorePatterns_UnreadableDirIsSkipped(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".cixignore":      "*.log\n",
		"locked/inner.go": "package locked\n",
	})
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("running as a user that ignores directory permissions")
	}

	ps, err := collectIgnorePatterns(context.Background(), root, DefaultFilter().ExcludeDirs, nil)
	if err != nil {
		t.Fatalf("an unlistable directory must not fail collection: %v", err)
	}
	if len(ps) != 1 {
		t.Fatalf("expected the root rule to survive, got %d patterns", len(ps))
	}
}

func TestCollectIgnorePatterns_HonoursContextCancellation(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a/b/c/x.go": "package c\n"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := collectIgnorePatterns(ctx, root, DefaultFilter().ExcludeDirs, nil); err == nil {
		t.Error("a cancelled context should stop the collection, not walk the whole tree")
	}
}

// TestIgnored_KnownDivergencesFromGit pins the two shapes where this matcher
// disagrees with `git check-ignore`. They are documented rather than fixed,
// and they are pinned here for two reasons: so the behaviour is discovered by
// a failing test rather than by a user, and because the two constrain each
// other. Dropping dirOnly patterns for file queries fixes the allowlist case
// and breaks the docs/* + !docs/api/ case (docs/api/other.md becomes ignored,
// where git keeps it), so a real fix needs a matcher that can tell "matches
// this path" from "matches an ancestor" — not a one-line tweak.
//
// Expected values below were taken from `git check-ignore -q --no-index` in a
// real repository, not from reading the matcher.
func TestIgnored_KnownDivergencesFromGit(t *testing.T) {
	t.Run("allowlist idiom under-excludes below depth 1", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, map[string]string{".cixignore": "*\n!*/\n!*.go\n"})
		f := filterFor(t, root)

		// Agrees with git at the top level.
		if !f.ignoredWithParents("top.txt") {
			t.Error("top.txt should be excluded")
		}
		if f.ignoredWithParents("top.go") {
			t.Error("top.go should be kept")
		}
		if f.ignoredWithParents("sub/x.go") {
			t.Error("sub/x.go should be kept")
		}
		// Diverges below it: git excludes both of these.
		if f.ignoredWithParents("sub/x.txt") {
			t.Error("behaviour changed: sub/x.txt is now excluded, which MATCHES git — " +
				"update the docs in doc/WORKSPACES.md and doc/CLI_REFERENCE.md")
		}
		if f.ignoredWithParents("a/b/c/deep.txt") {
			t.Error("behaviour changed: a/b/c/deep.txt is now excluded, which MATCHES git — " +
				"update the docs in doc/WORKSPACES.md and doc/CLI_REFERENCE.md")
		}
	})

	t.Run("character class negation is a literal set", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, map[string]string{".cixignore": "[!abc].txt\n"})
		f := filterFor(t, root)

		// git keeps a.txt; we drop it. This is the direction that costs a
		// tracked source file its place in the index.
		if !f.ignoredWithParents("a.txt") {
			t.Error("behaviour changed: a.txt is now kept, which MATCHES git — update the docs")
		}
		// git drops d.txt; we keep it.
		if f.ignoredWithParents("d.txt") {
			t.Error("behaviour changed: d.txt is now excluded, which MATCHES git — update the docs")
		}
	})

	t.Run("the shape a naive fix would break", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, map[string]string{".cixignore": "docs/*\n!docs/api/\n!docs/api/keep.md\n"})
		f := filterFor(t, root)

		if !f.ignoredWithParents("docs/other.md") {
			t.Error("docs/other.md should be excluded")
		}
		for _, p := range []string{"docs/api/keep.md", "docs/api/other.md"} {
			if f.ignoredWithParents(p) {
				t.Errorf("%s must stay indexed — this is what breaks if dirOnly patterns "+
					"are dropped for file queries to fix the allowlist idiom", p)
			}
		}
	})
}
