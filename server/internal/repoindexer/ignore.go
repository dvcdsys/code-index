package repoindexer

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// ignoreFileNames are the per-directory ignore files we honour, in LOAD
// ORDER. go-git resolves a path against the LAST matching pattern, so
// listing .cixignore second is what lets a cix-specific rule — including a
// "!" re-inclusion — override the .gitignore rule it collides with. The CLI
// cannot do this (cli/internal/discovery/discovery.go returns on the first
// matcher that hits), so this is one place the server is deliberately more
// capable rather than merely equivalent.
var ignoreFileNames = []string{".gitignore", ".cixignore"}

// collectIgnorePatterns walks rootDir and returns every ignore pattern in the
// tree, ordered by ASCENDING priority as gitignore.NewMatcher requires:
// shallower directories first, and within one directory .gitignore before
// .cixignore.
//
// Why a separate pre-pass instead of collecting during the main index walk:
// IndexDir has two drivers, and the incremental one never traverses
// directories at all — it is handed explicit repo-relative paths by
// repocloner's tree diff. A per-directory matcher stack (what the CLI builds)
// has nothing to hang off in that mode. One flat, domain-scoped pattern set
// serves both, because gitignore.Pattern carries the directory it came from
// as its domain and refuses to match outside it.
//
// excludeDirs prunes the collection the same way FileFilter prunes the index
// walk, so we never descend into node_modules to look for an ignore file.
// Pruning is also PROGRESSIVE — a directory excluded by the patterns gathered
// so far is not descended into either, mirroring gitignore.ReadPatterns.
// Without that, a `.cixignore` inside an already-ignored directory could carry
// a "!" that resurrects the subtree its parent just excluded.
func collectIgnorePatterns(ctx context.Context, rootDir string, excludeDirs []string, logger *slog.Logger) ([]gitignore.Pattern, error) {
	return collectIgnore(ctx, rootDir, excludeDirs, nil, logger)
}

// collectIgnorePatternsFor collects only the patterns that can possibly affect
// the given repo-relative paths, by descending the ancestor chains of those
// paths instead of the whole tree.
//
// This is exact rather than approximate, and it takes both halves of the
// argument. Domain: a pattern's domain is the directory its file sits in, and
// Match returns NoMatch for any path that does not start with that domain, so
// an ignore file outside a path's ancestor chain can never change the answer
// for it. Ordering: the restricted list is a strict subsequence of the full
// one — same pre-order walk, subtrees skipped rather than reordered — and the
// dropped patterns cannot match the queried paths, so go-git's last-match-wins
// lands on the same pattern either way. Progressive pruning agrees too, since
// a directory's prune decision depends only on patterns whose domain is one of
// its own ancestors, and those are collected in both modes.
//
// The incremental driver only ever asks about the paths in its change set,
// which is why it can afford this.
//
// It matters because the full walk is O(directories) while a push is usually
// O(3 files): on a repo with thousands of directories the unrestricted
// collection added a fixed cost to every webhook — inside the repo read lock,
// so a concurrent clone job waited on it too.
func collectIgnorePatternsFor(ctx context.Context, rootDir string, excludeDirs []string, logger *slog.Logger, groups ...[]string) ([]gitignore.Pattern, error) {
	want := ancestorDirSet(groups...)
	return collectIgnore(ctx, rootDir, excludeDirs, want, logger)
}

// ancestorDirSet returns every directory that could hold an ignore file
// affecting one of the given paths, as slash-joined repo-relative keys. The
// repo root is implicit — collection always starts there.
func ancestorDirSet(groups ...[]string) map[string]bool {
	out := make(map[string]bool)
	for _, group := range groups {
		for _, rel := range group {
			dir := path.Dir(path.Clean(rel))
			for dir != "." && dir != "/" && dir != "" {
				out[dir] = true
				dir = path.Dir(dir)
			}
		}
	}
	return out
}

// collectIgnore is the shared implementation. want == nil means "the whole
// tree"; otherwise only directories in the set are descended into. Keeping one
// walker means the ordering, pruning and parsing rules cannot drift between
// the two modes.
func collectIgnore(ctx context.Context, rootDir string, excludeDirs []string, want map[string]bool, logger *slog.Logger) ([]gitignore.Pattern, error) {
	if logger == nil {
		logger = slog.Default()
	}
	excluded := make(map[string]bool, len(excludeDirs))
	for _, d := range excludeDirs {
		excluded[strings.ToLower(d)] = true
	}
	var ps []gitignore.Pattern
	if err := collectIgnoreDir(ctx, rootDir, nil, excluded, want, logger, &ps); err != nil {
		return nil, err
	}
	return ps, nil
}

// collectIgnoreDir appends dir's own patterns, then recurses into its
// subdirectories. Pre-order is what produces the ascending-priority ordering:
// a directory's patterns are always appended before any of its descendants'.
// Sibling order is irrelevant — disjoint domains cannot match each other's
// paths.
func collectIgnoreDir(ctx context.Context, dir string, domain []string, excluded map[string]bool, want map[string]bool, logger *slog.Logger, ps *[]gitignore.Pattern) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Log and carry on, exactly as the index walk does for the same class
		// of problem. A directory we cannot list is a directory the walk
		// cannot list either, so its subtree is not going to be indexed and
		// missing its ignore files cannot cause over-indexing. A missing root
		// is the normal case when a clone failed before this ran.
		if !os.IsNotExist(err) {
			logger.Warn("repoindexer: ignore collection skipped", "dir", dir, "err", err)
		}
		return nil
	}

	// This pass runs on every index job, incremental ones included, so the
	// directory that has no ignore file — nearly all of them — must cost
	// nothing beyond the ReadDir we already did. Scanning the entries we hold
	// beats two speculative os.Open syscalls per directory; on a repo with
	// thousands of directories that is the difference between a rounding
	// error and a visible pause. Outer loop over the names, not the entries,
	// so .gitignore is still parsed before .cixignore.
	for _, name := range ignoreFileNames {
		found := false
		for _, e := range entries {
			// IsDir is false for a symlink, so a symlinked ignore file is
			// still picked up and followed by the open below.
			if !e.IsDir() && e.Name() == name {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		// Unlike an unlistable directory, a listed-but-unreadable ignore file
		// IS fatal: the walk can still descend here, so proceeding would index
		// files a rule we know exists was meant to exclude. Consequence worth
		// recognising rather than guarding against — the job retries, so a
		// PERMANENTLY unreadable ignore file parks the repo in a failing loop
		// with "collect ignore patterns:" in last_error. Unreachable in
		// practice, since the server wrote these files into its own clone dir.
		patterns, err := readIgnoreFile(filepath.Join(dir, name), domain)
		if err != nil {
			return err
		}
		*ps = append(*ps, patterns...)
	}

	// Snapshot the matcher once: anything appended below belongs to a deeper
	// domain and cannot match a sibling of the current directory.
	m := gitignore.NewMatcher(*ps)
	for _, e := range entries {
		// DirEntry reports symlinks as non-directories, so this skips
		// symlinked directories exactly like filepath.WalkDir does.
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if excluded[strings.ToLower(name)] {
			continue
		}
		child := append(append([]string{}, domain...), name)
		if want != nil && !want[strings.Join(child, "/")] {
			continue
		}
		if m.Match(child, true) {
			continue
		}
		if err := collectIgnoreDir(ctx, filepath.Join(dir, name), child, excluded, want, logger, ps); err != nil {
			return err
		}
	}
	return nil
}

// readIgnoreFile parses one ignore file. A missing file is not an error: the
// caller only calls this for names it saw in the directory listing, so this
// only fires if the file vanished in between.
//
// gitignore.ParsePattern handles "!", trailing spaces and a trailing "/", but
// NOT comments or blank lines: go-git filters those in its own unexported
// reader, so we have to repeat it here. Reading through bufio.Scanner also
// gets CRLF handling for free (ScanLines drops a trailing \r), which matters
// for ignore files authored on Windows.
func readIgnoreFile(path string, domain []string) ([]gitignore.Pattern, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var ps []gitignore.Pattern
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		ps = append(ps, gitignore.ParsePattern(line, domain))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return ps, nil
}

// IsIgnoreFile reports whether a repo-relative path names one of the ignore
// files this package honours. Exported so the job layer can spot an
// ignore-rule change in a git tree diff without duplicating the file names —
// if the list above ever grows, the escalation rule grows with it.
func IsIgnoreFile(relPath string) bool {
	base := path.Base(relPath)
	for _, name := range ignoreFileNames {
		if base == name {
			return true
		}
	}
	return false
}
