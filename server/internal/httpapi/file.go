package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dvcdsys/code-index/server/internal/access"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/langdetect"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/repocloner"
)

// File/tree read limits. These cap a single response so one request can't pull
// an arbitrarily large blob or directory into memory; hitting a cap sets
// `truncated: true` rather than erroring.
const (
	maxFileBytes   = 2 << 20 // 2 MiB read from disk per file request
	maxFileLines   = 5000    // max lines returned in one file response
	maxTreeEntries = 2000    // max directory entries returned in one tree response
)

// errUnsafePath is returned by safeJoin when the requested path escapes the
// repository root (parent traversal, absolute path, or a symlink pointing out).
var errUnsafePath = errors.New("path escapes repository root")

// ReadProjectFile — POST /api/v1/projects/{path}/file. Reads a file (whole or a
// line range) from an EXTERNAL project's on-disk git checkout. Local projects
// have no files on the server and return 409.
func (s *Server) ReadProjectFile(w http.ResponseWriter, r *http.Request, _ openapi.ProjectHash) {
	p := s.requireProjectAccess(w, r)
	if p == nil {
		return
	}
	var body openapi.FileReadRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body")
		return
	}
	if strings.TrimSpace(body.File) == "" {
		writeError(w, http.StatusUnprocessableEntity, "file is required")
		return
	}
	if body.Start != nil && body.End != nil && *body.End < *body.Start {
		writeError(w, http.StatusUnprocessableEntity, "end must be >= start")
		return
	}

	cloneDir, ok := s.externalCheckoutDir(w, r, p)
	if !ok {
		return
	}

	// Read-lock: never observe the worktree mid git-reset (the clone worker
	// holds the matching write-lock around CloneOrFetch). Same key the writer
	// uses (path_hash of the external project's host_path). Taken BEFORE
	// safeJoin so the symlink-escape check (EvalSymlinks) and the stat/open all
	// run against one consistent worktree — otherwise a concurrent reset could
	// swap in an escaping symlink between validation and open (TOCTOU).
	mu := s.Deps.RepoLocks.For(projects.HashPath(p.HostPath))
	mu.RLock()
	defer mu.RUnlock()

	full, err := safeJoin(cloneDir, body.File)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid file path")
		return
	}

	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory; use /tree")
		return
	}

	// Read at most maxFileBytes+1 so we can detect (and flag) oversize files.
	f, err := os.Open(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		f.Close()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	byteTruncated := len(data) > maxFileBytes

	// total_lines must reflect the whole file on disk, not just the portion we
	// buffer for content. Count newlines across everything we read; when the
	// byte cap cut the file short, stream the remainder (past what we keep for
	// content) purely to keep counting — O(1) memory, single pass over the file.
	totalNewlines := bytes.Count(data, []byte{'\n'})
	var lastByte byte
	if len(data) > 0 {
		lastByte = data[len(data)-1]
	}
	if byteTruncated {
		tailNL, tailLast, cerr := countTailNewlines(f)
		if cerr != nil {
			f.Close()
			writeError(w, http.StatusInternalServerError, cerr.Error())
			return
		}
		totalNewlines += tailNL
		if tailLast != 0 {
			lastByte = tailLast
		}
	}
	f.Close()

	// Lines = newlines, plus one more when the final line isn't newline-terminated.
	total := totalNewlines
	if lastByte != 0 && lastByte != '\n' {
		total++
	}

	if byteTruncated {
		data = data[:maxFileBytes]
	}
	// sliceLines slices content out of the buffered bytes only; its own line
	// count is bounded by the buffer, so we take the accurate `total` computed
	// above and ignore the buffer-local count sliceLines returns.
	content, startLine, endLine, _, lineTruncated := sliceLines(data, body.Start, body.End)

	// If the byte cap cut the file off before the requested start line, the
	// window is empty for a reason the caller can't see (the lines exist on
	// disk, past the 2 MiB we read). Fail loudly rather than returning a
	// confusing empty 200. endLine < startLine only when no lines were selected;
	// pairing it with byteTruncated isolates the "range past the cap" case from a
	// genuinely empty file (which is not byte-truncated).
	if byteTruncated && endLine < startLine {
		writeError(w, http.StatusBadRequest,
			"requested line range begins beyond the portion of this file the server can read "+
				"(files are capped at 2 MiB); narrow the range or start earlier in the file.")
		return
	}

	relFile := filepath.ToSlash(filepath.Clean(strings.TrimSpace(body.File)))
	lang := langdetect.Detect(relFile)
	var langPtr *string
	if lang != "" {
		langPtr = &lang
	}
	writeJSON(w, http.StatusOK, openapi.FileContent{
		FilePath:   relFile,
		Language:   langPtr,
		StartLine:  startLine,
		EndLine:    endLine,
		TotalLines: total,
		Truncated:  byteTruncated || lineTruncated,
		Content:    content,
	})
}

// ListProjectTree — POST /api/v1/projects/{path}/tree. Lists one level of a
// directory in an EXTERNAL project's on-disk git checkout (ls-like, no
// recursion). Local projects return 409.
func (s *Server) ListProjectTree(w http.ResponseWriter, r *http.Request, _ openapi.ProjectHash) {
	p := s.requireProjectAccess(w, r)
	if p == nil {
		return
	}
	var body openapi.TreeRequest
	// Body is optional (empty = repo root), so an empty body (io.EOF) is fine —
	// but a malformed or wrong-typed body must not be silently treated as "list
	// the root"; reject it like /file does.
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	dir := ""
	if body.Dir != nil {
		dir = *body.Dir
	}

	cloneDir, ok := s.externalCheckoutDir(w, r, p)
	if !ok {
		return
	}

	// Read-lock before safeJoin (symlink check) + stat + list, so all of them
	// see one consistent worktree even if the clone worker resets concurrently.
	mu := s.Deps.RepoLocks.For(projects.HashPath(p.HostPath))
	mu.RLock()
	defer mu.RUnlock()

	full, err := safeJoin(cloneDir, dir)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid directory path")
		return
	}

	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "directory not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a file; use /file")
		return
	}

	dirEntries, err := os.ReadDir(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	atRoot := full == cloneDir
	entries := make([]openapi.TreeEntry, 0, len(dirEntries))
	truncated := false
	for _, de := range dirEntries {
		name := de.Name()
		if atRoot && name == ".git" {
			continue // never expose the git metadata dir
		}
		if len(entries) >= maxTreeEntries {
			truncated = true
			break
		}
		entry := openapi.TreeEntry{Name: name}
		if de.IsDir() {
			entry.Type = openapi.Dir
		} else {
			entry.Type = openapi.File
			if fi, ferr := de.Info(); ferr == nil {
				sz := int(fi.Size())
				entry.Size = &sz
			}
			if lang := langdetect.Detect(name); lang != "" {
				l := lang
				entry.Language = &l
			}
		}
		entries = append(entries, entry)
	}
	// Stable order: dirs first, then files, each alphabetical — predictable for
	// an agent paging through a tree.
	sort.SliceStable(entries, func(i, j int) bool {
		if (entries[i].Type == openapi.Dir) != (entries[j].Type == openapi.Dir) {
			return entries[i].Type == openapi.Dir
		}
		return entries[i].Name < entries[j].Name
	})

	writeJSON(w, http.StatusOK, openapi.DirectoryListing{
		Dir:       filepath.ToSlash(normalizeRel(dir)),
		Entries:   entries,
		Truncated: truncated,
	})
}

// externalCheckoutDir resolves the on-disk checkout root for an EXTERNAL
// project, writing the appropriate 409 and returning ok=false for local
// projects or when the checkout isn't on disk yet. The caller must already
// have passed requireProjectAccess.
func (s *Server) externalCheckoutDir(w http.ResponseWriter, r *http.Request, p *projects.Project) (string, bool) {
	external, err := access.IsProjectExternal(r.Context(), s.Deps.DB, p.HostPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return "", false
	}
	if !external {
		writeError(w, http.StatusConflict,
			"this project is local; the cix server does not keep its files on disk. "+
				"Read it directly with your own filesystem tools (Read / cat / ls). "+
				"cix file/tree work only for external (GitHub-backed) or workspace repos the harness cannot see.")
		return "", false
	}
	cloneDir := repocloner.LocalDirFor(s.Deps.DataDir, projects.HashPath(p.HostPath))
	if fi, err := os.Stat(cloneDir); err != nil || !fi.IsDir() {
		writeError(w, http.StatusConflict,
			"this project's checkout is not yet available on the server "+
				"(clone/index may still be in progress); retry shortly.")
		return "", false
	}
	return cloneDir, true
}

// safeJoin resolves a repo-relative path against root, rejecting any attempt to
// escape root via parent traversal, an absolute path, or a symlink that points
// outside. Returns the absolute on-disk path. A non-existent leaf is allowed
// (the caller's stat/open then produces 404); only escapes are rejected.
func safeJoin(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if filepath.IsAbs(rel) {
		return "", errUnsafePath
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == "" {
		return root, nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errUnsafePath
	}
	// Never expose the git metadata dir (consistent with the tree listing, which
	// hides it at root). Only the first segment can be the clone's own .git — a
	// nested ".git" component would be a committed path git itself refuses to
	// create, so checking the first segment is both precise and sufficient.
	first, _, _ := strings.Cut(filepath.ToSlash(clean), "/")
	if first == ".git" {
		return "", errUnsafePath
	}
	full := filepath.Join(root, clean)

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if !withinRoot(absRoot, absFull) {
		return "", errUnsafePath
	}
	// Symlink defence: if the path resolves, it must still sit inside root.
	// A non-existent path is fine — stat/open will 404 it.
	resolved, err := filepath.EvalSymlinks(absFull)
	if err != nil {
		if os.IsNotExist(err) {
			return absFull, nil
		}
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", err
	}
	if !withinRoot(resolvedRoot, resolved) {
		return "", errUnsafePath
	}
	return absFull, nil
}

// withinRoot reports whether target is root itself or lives under it.
func withinRoot(root, target string) bool {
	return target == root || strings.HasPrefix(target, root+string(os.PathSeparator))
}

// normalizeRel renders a repo-relative dir for the response: "" for the root,
// otherwise the cleaned relative path.
func normalizeRel(rel string) string {
	clean := filepath.Clean(strings.TrimSpace(rel))
	if clean == "." || clean == string(os.PathSeparator) {
		return ""
	}
	return clean
}

// sliceLines splits data into lines and returns the [start,end] window
// (1-based, inclusive), the file's total line count, and whether the line cap
// truncated the window. start/end nil means file start/end respectively.
func sliceLines(data []byte, start, end *int) (content string, outStart, outEnd, total int, truncated bool) {
	lines := strings.Split(string(data), "\n")
	// strings.Split leaves a trailing "" for a file ending in newline; drop it
	// so total reflects real lines.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	total = len(lines)

	s := 1
	if start != nil && *start > 1 {
		s = *start
	}
	e := total
	if end != nil && *end > 0 && *end < e {
		e = *end
	}
	if total == 0 || s > total {
		// Empty file, or range entirely past EOF → empty content.
		return "", s, s - 1, total, false
	}
	if e < s {
		e = s
	}
	sel := lines[s-1 : e]
	if len(sel) > maxFileLines {
		sel = sel[:maxFileLines]
		e = s + maxFileLines - 1
		truncated = true
	}
	return strings.Join(sel, "\n"), s, e, total, truncated
}

// countTailNewlines streams f from its current offset to EOF, returning the
// number of '\n' bytes and the last byte seen (0 if the tail was empty). It is
// used to count the lines of a file past the byte cap without buffering them.
func countTailNewlines(f *os.File) (count int, last byte, err error) {
	buf := make([]byte, 32*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			count += bytes.Count(buf[:n], []byte{'\n'})
			last = buf[n-1]
		}
		if rerr == io.EOF {
			return count, last, nil
		}
		if rerr != nil {
			return count, last, rerr
		}
	}
}
