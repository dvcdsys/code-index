package repoindexer

import (
	"bufio"
	"fmt"
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
func collectIgnorePatterns(rootDir string, excludeDirs []string) ([]gitignore.Pattern, error) {
	excluded := make(map[string]bool, len(excludeDirs))
	for _, d := range excludeDirs {
		excluded[strings.ToLower(d)] = true
	}
	var ps []gitignore.Pattern
	if err := collectIgnoreDir(rootDir, nil, excluded, &ps); err != nil {
		return nil, err
	}
	return ps, nil
}

// collectIgnoreDir appends dir's own patterns, then recurses into its
// subdirectories. Pre-order is what produces the ascending-priority ordering:
// a directory's patterns are always appended before any of its descendants'.
// Sibling order is irrelevant — disjoint domains cannot match each other's
// paths.
func collectIgnoreDir(dir string, domain []string, excluded map[string]bool, ps *[]gitignore.Pattern) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A missing root is normal in tests and when a clone failed before
		// this ran; an unreadable subtree is already tolerated by the index
		// walk, which logs and skips it. Neither should fail the job.
		if os.IsNotExist(err) || os.IsPermission(err) {
			return nil
		}
		return fmt.Errorf("read dir %s: %w", dir, err)
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
		if m.Match(child, true) {
			continue
		}
		if err := collectIgnoreDir(filepath.Join(dir, name), child, excluded, ps); err != nil {
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
