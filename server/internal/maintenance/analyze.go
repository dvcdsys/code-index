package maintenance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// analysisBudget bounds a scan. The HTTP server's WriteTimeout is 60s, so the
// scan has to finish inside that or the response is cut off mid-write. We fail
// closed on expiry (ErrAnalysisTimeout) rather than returning what we have:
// a partial analysis is indistinguishable from a complete one once it reaches
// the confirmation dialog, and the admin would be approving a delete against
// numbers that silently omit half the server.
const analysisBudget = 45 * time.Second

// staleJobRetention is how long a finished job row is kept before it counts as
// garbage. Long enough that a week-old failure is still there to look at.
const staleJobRetention = 30 * 24 * time.Hour

// ggufSettleWindow protects a model file that is still being written. The
// downloader writes <name>.partial and renames, but a rename that just landed
// looks exactly like an idle file, so recent mtime is treated as in-flight.
const ggufSettleWindow = 5 * time.Minute

// pathHashRE matches projects.HashPath output: the first 8 bytes of a SHA-1,
// hex-encoded. A live checkout directory is always named this way.
var pathHashRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

// Analyze scans for reclaimable garbage and caches the result under a fresh
// analysis id.
//
// Concurrent calls collapse: the first one scans, the rest wait and return its
// result. Analyze always recomputes when it is the one scanning — the TTL
// governs how long the returned id stays redeemable by Clean, not how long a
// response may be reused.
func (s *Service) Analyze(ctx context.Context) (*Analysis, error) {
	// Snapshot what is cached before waiting, so a follower can tell the
	// leader's fresh result from whatever happened to be sitting there.
	before := s.cache.latestID()
	done, wait := s.cache.beginScan()
	if done == nil {
		select {
		case <-wait:
			// Only adopt the leader's result if it is actually new AND still
			// inside its TTL. Without both checks a follower could be handed a
			// twenty-minute-old analysis — the leader's scan having failed, or
			// its result having been consumed by a Clean meanwhile — and would
			// return 200 with a stale picture whose id then 409s.
			if a := s.cache.latest(); a != nil && a.ID != before && s.cache.fresh(a) {
				return a, nil
			}
			// The leader produced nothing usable, so fall through and scan
			// ourselves — without re-entering Analyze. Recursing here would
			// add a stack frame per failed round, and a scan that keeps
			// failing under a steady stream of requests turns that into
			// unbounded growth. Falling through also means each caller gets
			// its own error instead of queueing behind a broken leader, which
			// is what you want precisely when things are failing.
			//
			// Same shape as Usage; the two are deliberately identical.
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else {
		defer done()
	}

	ctx, cancel := context.WithTimeout(ctx, analysisBudget)
	defer cancel()

	started := s.now()
	a, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	a.ID = newAnalysisID()
	a.GeneratedAt = started
	a.ExpiresAt = started.Add(analysisTTL)
	a.DurationMS = s.now().Sub(started).Milliseconds()
	for _, c := range a.Categories {
		a.TotalReclaimableBytes += c.SizeBytes
	}
	s.cache.put(a)
	return a, nil
}

// scanState is the live picture every scanner reconciles against, read once so
// five scanners do not issue the same queries five times.
type scanState struct {
	// liveCollections maps chromem collection name -> host_path, for every
	// row currently in `projects`.
	liveCollections map[string]string
	// liveHashes is the set of projects.HashPath values currently in use.
	liveHashes map[string]struct{}
	// activeIndexJobs is the number of pending/running clone or index jobs.
	// Non-zero disables the orphan-collection category wholesale; see the
	// comment on scanOrphanCollections.
	activeIndexJobs int
}

func (s *Service) readScanState(ctx context.Context) (*scanState, error) {
	st := &scanState{
		liveCollections: map[string]string{},
		liveHashes:      map[string]struct{}{},
	}
	if s.d.DB == nil {
		return st, nil
	}

	rows, err := s.d.DB.QueryContext(ctx, `SELECT host_path FROM projects`)
	if err != nil {
		return nil, fmt.Errorf("maintenance: list projects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hostPath string
		if err := rows.Scan(&hostPath); err != nil {
			return nil, fmt.Errorf("maintenance: scan project: %w", err)
		}
		st.liveCollections[vectorstore.CollectionName(hostPath)] = hostPath
		// Recompute the hash rather than trusting the stored path_hash column:
		// it is nullable and was backfilled by a migration, and being wrong
		// here means deleting a live checkout.
		st.liveHashes[projects.HashPath(hostPath)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("maintenance: iterate projects: %w", err)
	}

	if err := s.d.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM jobs
		WHERE status IN ('pending','running') AND type IN ('clone_repo','index_repo')
	`).Scan(&st.activeIndexJobs); err != nil {
		return nil, fmt.Errorf("maintenance: count active jobs: %w", err)
	}
	return st, nil
}

func (s *Service) scan(ctx context.Context) (*Analysis, error) {
	st, err := s.readScanState(ctx)
	if err != nil {
		return nil, err
	}
	a := &Analysis{}
	// Per-scanner timing. Each scanner walks a different tree and any of them
	// can dominate on a given deployment; without this the only signal an
	// operator gets from a slow analysis is the overall timeout.
	scanStarted := s.now()
	timed := func(id CategoryID, fn func() (Category, []string)) (Category, []string) {
		began := s.now()
		c, w := fn()
		s.d.Logger.Debug("maintenance scan phase",
			"category", id, "items", len(c.Items), "ms", s.now().Sub(began).Milliseconds())
		return c, w
	}
	add := func(c Category, warnings []string) {
		// Scanners fill c.Items with everything they found; the complete list
		// moves to c.all (what Clean acts on) and c.Items keeps only the
		// sample the browser renders.
		c.all = c.Items
		if c.all == nil {
			// encoding/json turns a nil slice into `null`, not `[]`, and the
			// schema declares items as a required array. A client that trusts
			// the schema then does items.length on null and dies. Normalise
			// here so the wire format matches the contract for every category,
			// including the ones that found nothing.
			c.all = []Item{}
		}
		c.Items = c.all
		c.SizeBytes = 0
		for _, it := range c.all {
			c.SizeBytes += it.SizeBytes
		}
		c.ItemCount = len(c.all)
		if len(c.all) > maxItemsPerCategory {
			c.Items = c.all[:maxItemsPerCategory]
			c.ItemsTruncated = true
		}
		if c.ItemCount == 0 {
			c.DefaultSelected = false
		}
		a.Categories = append(a.Categories, c)
		a.Warnings = append(a.Warnings, warnings...)
	}

	add(timed(CatOrphanCollections, func() (Category, []string) { return s.scanOrphanCollections(ctx, st) }))
	if ctxDone(ctx) {
		return nil, ErrAnalysisTimeout
	}
	add(timed(CatOrphanRepos, func() (Category, []string) { return s.scanOrphanRepos(ctx, st) }))
	if ctxDone(ctx) {
		return nil, ErrAnalysisTimeout
	}
	add(timed(CatStaleNamespaces, func() (Category, []string) { return s.scanStaleNamespaces(ctx) }))
	if ctxDone(ctx) {
		return nil, ErrAnalysisTimeout
	}
	add(timed(CatStaleJobs, func() (Category, []string) { return s.scanStaleJobs(ctx) }))
	add(timed(CatUnusedModels, func() (Category, []string) { return s.scanUnusedModels(ctx) }))
	if ctxDone(ctx) {
		return nil, ErrAnalysisTimeout
	}
	s.d.Logger.Info("maintenance analysis complete",
		"ms", s.now().Sub(scanStarted).Milliseconds(), "categories", len(a.Categories))
	return a, nil
}

// scanOrphanCollections is the headline category: vector collections with no
// row in `projects`.
//
// It used to promise memory back as well as disk, because chromem held every
// document of every collection in the Go heap for the life of the process.
// The SQLite store does not: a collection costs disk and nothing else until it
// is searched. The category is still worth having — an abandoned index is
// hundreds of megabytes — but the RAM claim is gone and EstimatedRAMBytes
// stays zero rather than reporting an attribution that is no longer real.
//
// The guard is category-wide rather than per item, and that is a real
// limitation worth understanding. A queued job identifies its target by
// dedupe_key, which carries a SHA-1 path_hash; a collection identifies its
// target by an MD5 of the same host_path. Neither hash is invertible, and an
// orphan by definition has no `projects` row to join the two through — so
// there is no way to ask "is THIS collection the one that job is about to
// write to". Rather than guess, any active clone/index job turns the whole
// category off, and the description says why so the admin is not left
// wondering where their checkbox went.
func (s *Service) scanOrphanCollections(ctx context.Context, st *scanState) (Category, []string) {
	c := Category{
		ID:              CatOrphanCollections,
		Label:           "Orphaned vector collections",
		Description:     "Indexed vectors for projects that no longer exist. They sit in the vector database taking up disk until they are removed.",
		DefaultSelected: true,
	}
	var warnings []string

	vs := s.d.VectorStore
	if vs == nil {
		return c, []string{"vector store unavailable — orphaned collections were not scanned"}
	}

	for _, col := range vs.ListCollections() {
		if ctxDone(ctx) {
			break
		}
		if _, live := st.liveCollections[col.Name]; live {
			continue
		}
		// Never touch a collection this codebase did not create. Anything
		// without the project_ prefix was put there by something else.
		if !strings.HasPrefix(col.Name, "project_") {
			warnings = append(warnings, fmt.Sprintf("ignoring unrecognised collection %q", col.Name))
			continue
		}
		c.Items = append(c.Items, Item{
			Key:    col.Name,
			Label:  col.Name,
			Detail: fmt.Sprintf("%d documents", col.Documents),
			// The store reports the logical size of the rows. It excludes
			// index entries and page slack, and the file does not shrink
			// until a VACUUM — the pages are recycled by the next index.
			SizeBytes:  col.SizeBytes,
			collection: col.Name,
		})
	}

	if st.activeIndexJobs > 0 && len(c.Items) > 0 {
		c.DefaultSelected = false
		c.Description += " Disabled right now: an index or clone job is running, and an orphan cannot be matched to a running job, so cleaning is held back until the queue is idle."
		warnings = append(warnings, "orphaned collections were found but an index/clone job is active — they will be skipped until it finishes")
	}
	return c, warnings
}

// scanOrphanRepos finds cloned checkouts whose project is gone.
func (s *Service) scanOrphanRepos(ctx context.Context, st *scanState) (Category, []string) {
	c := Category{
		ID:              CatOrphanRepos,
		Label:           "Orphaned repository clones",
		Description:     "Git checkouts on disk for projects that no longer exist. Disk only — re-adding the project re-clones it.",
		DefaultSelected: true,
	}
	root := s.reposRoot()
	if root == "" {
		return c, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, []string{fmt.Sprintf("could not read %s: %v", root, err)}
	}

	for _, e := range entries {
		if ctxDone(ctx) {
			break
		}
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		detail := ""
		if pathHashRE.MatchString(name) {
			if _, live := st.liveHashes[name]; live {
				continue
			}
		} else {
			// Not a path_hash, so it cannot be a checkout this server would
			// ever look up — older builds named these directories with UUIDs.
			// Safe to reclaim, but labelled so it is obvious what it is.
			detail = "legacy directory layout"
		}
		dir := filepath.Join(root, name)
		c.Items = append(c.Items, Item{
			Key:       name,
			Label:     repoLabel(dir, name),
			Detail:    detail,
			SizeBytes: dirSizeOrZero(ctx, dir),
			path:      dir,
		})
	}
	return c, nil
}

// repoLabel digs the origin URL out of a checkout so the confirmation dialog
// shows something a human recognises instead of a hash. Best effort.
func repoLabel(dir, fallback string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		return fallback
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "url = "); ok {
			return strings.TrimSpace(after)
		}
	}
	return fallback
}

// scanStaleNamespaces finds vector-store namespace directories left behind by
// a provider or model switch. Switching providers opens a new, dimension-
// isolated directory and simply abandons the old one.
func (s *Service) scanStaleNamespaces(ctx context.Context) (Category, []string) {
	c := Category{
		ID:              CatStaleNamespaces,
		Label:           "Abandoned provider namespaces",
		Description:     "Vector stores for embedding providers or models that are no longer active. Nothing reads them; they stay on disk forever.",
		DefaultSelected: true,
	}
	if s.d.Cfg == nil {
		return c, nil
	}
	active := s.activeNamespaceDirs()
	if len(active) == 0 {
		// Refuse to classify anything. With no known active namespace every
		// directory looks stale, and the first one we would delete is the live
		// index.
		return c, []string{"active embedding namespace is unknown — no namespace was classified as stale"}
	}

	// Two trees hold namespaces: the live SQLite databases under VectorsDir,
	// and the legacy chromem gob directories under ChromaPersistDir. Both leak
	// a namespace per abandoned model, so both are scanned — while the ACTIVE
	// namespace in EITHER tree is protected, which is what keeps the
	// pre-migration gob files (the rollback path) off the list.
	var warnings []string
	for _, base := range s.namespaceBases() {
		for _, dir := range base.leaves(base.root) {
			if ctxDone(ctx) {
				break
			}
			if isActiveNamespace(dir, active) {
				continue
			}
			c.Items = append(c.Items, Item{
				Key:       dir,
				Label:     strings.TrimPrefix(dir, filepath.Dir(base.root)+string(os.PathSeparator)),
				Detail:    base.detail,
				SizeBytes: dirSizeOrZero(ctx, dir),
				path:      dir,
			})
		}
	}

	// A namespace bigger than the live one is a warning sign, not routine
	// garbage. The realistic accident is booting with the wrong embedding
	// model: the server opens a fresh, nearly empty namespace, and the real
	// index — every vector the operator has ever built — instantly looks
	// abandoned. Deleting it would be correct by the letter of the rule and
	// catastrophic in practice, so the category stops being pre-ticked and
	// says why.
	// Sizing the active namespace means walking every document in the index,
	// so only pay for it when there is actually something to compare against.
	if len(c.Items) > 0 {
		var activeSize int64
		for _, dir := range active {
			activeSize += dirSizeOrZero(ctx, dir)
		}
		for _, it := range c.Items {
			if it.SizeBytes > activeSize {
				c.DefaultSelected = false
				c.Description += " Not pre-selected: one of these is larger than the namespace in use, which usually means the server booted with a different embedding model than the index was built with. Check the model setting before removing anything."
				warnings = append(warnings, fmt.Sprintf(
					"inactive namespace %q (%d bytes) is larger than the active one (%d bytes) — verify the embedding model is what you expect",
					it.Label, it.SizeBytes, activeSize))
				break
			}
		}
	}
	return c, warnings
}

// namespaceBase is one tree that holds per-embedding-identity namespaces.
type namespaceBase struct {
	root   string
	detail string
	leaves func(root string) []string
}

// namespaceBases returns the trees to scan, skipping the ones this deployment
// does not have.
func (s *Service) namespaceBases() []namespaceBase {
	var out []namespaceBase
	if s.d.Cfg == nil {
		return nil
	}
	if dir := s.d.Cfg.VectorsDir; dir != "" {
		out = append(out, namespaceBase{
			root:   filepath.Clean(dir),
			detail: "inactive namespace",
			leaves: vectorNamespaceLeaves,
		})
	}
	if dir := s.d.Cfg.ChromaPersistDir; dir != "" {
		out = append(out, namespaceBase{
			root:   filepath.Clean(dir),
			detail: "inactive namespace (pre-migration format)",
			leaves: namespaceLeaves,
		})
	}
	return out
}

// isActiveNamespace reports whether dir is, contains, or lives inside one of
// the active namespace directories.
func isActiveNamespace(dir string, active []string) bool {
	for _, a := range active {
		if dir == a || isAncestor(dir, a) || isAncestor(a, dir) {
			return true
		}
	}
	return false
}

// vectorNamespaceLeaves returns the directories holding a SQLite vector store.
// A leaf is a directory containing the database file; an entirely empty
// directory below the container is a leftover namespace whose database is
// already gone (see namespaceLeaves for the same reasoning).
func vectorNamespaceLeaves(base string) []string {
	var out []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 4 {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		if len(entries) == 0 {
			if depth >= 1 {
				out = append(out, dir)
			}
			return
		}
		var subdirs []string
		for _, e := range entries {
			if e.IsDir() {
				subdirs = append(subdirs, filepath.Join(dir, e.Name()))
				continue
			}
			if e.Name() == vectorstore.DBFileName {
				out = append(out, dir)
				return
			}
		}
		for _, sd := range subdirs {
			walk(sd, depth+1)
		}
	}
	walk(base, 0)
	sort.Strings(out)
	return out
}

// namespaceLeaves returns the directories that hold collections. A namespace
// leaf is a directory whose children are chromem collection directories
// (8 hex characters); the levels above it are the provider kind and model
// slug that ChromaDirFor nests.
func namespaceLeaves(base string) []string {
	var out []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 4 {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		// A completely empty directory below the container is a leftover
		// namespace whose collections are already gone — zero bytes, but
		// listing it is what lets a clean actually leave nothing behind
		// instead of "nothing except some empty directories". Only ever
		// when it holds NOTHING at all: a directory with loose files in it
		// is something we do not understand, and unexplained files are not
		// ours to delete.
		if len(entries) == 0 {
			if depth >= 1 {
				out = append(out, dir)
			}
			return
		}
		var subdirs []string
		collectionLike := 0
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			subdirs = append(subdirs, filepath.Join(dir, e.Name()))
			if collectionDirRE.MatchString(e.Name()) {
				collectionLike++
			}
		}
		if len(subdirs) == 0 {
			return
		}
		if collectionLike == len(subdirs) {
			out = append(out, dir)
			return
		}
		for _, sd := range subdirs {
			walk(sd, depth+1)
		}
	}
	walk(base, 0)
	sort.Strings(out)
	return out
}

// collectionDirRE matches chromem's on-disk collection directory name — the
// first 4 bytes of a SHA-256, hex-encoded.
var collectionDirRE = regexp.MustCompile(`^[0-9a-f]{8}$`)

// isAncestor reports whether parent contains child.
func isAncestor(parent, child string) bool {
	return strings.HasPrefix(child, parent+string(os.PathSeparator))
}

// scanStaleJobs counts finished rows in the work queue. The table has no
// foreign key to projects and no retention policy, so it only ever grows.
func (s *Service) scanStaleJobs(ctx context.Context) (Category, []string) {
	c := Category{
		ID:              CatStaleJobs,
		Label:           "Finished job records",
		Description:     fmt.Sprintf("Completed and failed queue rows older than %d days. Frees database pages; the file itself only shrinks after a VACUUM.", int(staleJobRetention.Hours()/24)),
		DefaultSelected: true,
	}
	if s.d.DB == nil {
		return c, nil
	}
	cutoff := s.now().Add(-staleJobRetention).UTC().Format(time.RFC3339)
	var count int
	var bytes int64
	err := s.d.DB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(LENGTH(payload) + LENGTH(COALESCE(last_error,''))), 0)
		FROM jobs
		WHERE status IN ('completed','failed')
		  AND COALESCE(completed_at, created_at) < ?
	`, cutoff).Scan(&count, &bytes)
	if err != nil {
		return c, []string{fmt.Sprintf("could not count finished jobs: %v", err)}
	}
	if count == 0 {
		return c, nil
	}
	c.Items = append(c.Items, Item{
		Key:       "jobs",
		Label:     "finished job rows",
		Detail:    fmt.Sprintf("%d rows", count),
		SizeBytes: bytes,
	})
	return c, nil
}

// scanUnusedModels finds cached GGUF files other than the active model.
func (s *Service) scanUnusedModels(ctx context.Context) (Category, []string) {
	c := Category{
		ID:          CatUnusedModels,
		Label:       "Unused model files",
		Description: "Cached GGUF weights other than the active embedding model. Off by default: getting one back means a multi-minute download.",
		// Deliberately not pre-selected, and flagged so the dialog can warn.
		DefaultSelected: false,
		Destructive:     true,
	}
	cacheDir := s.activeGGUFCacheDir()
	if cacheDir == "" {
		return c, nil
	}
	activeDir := ""
	if repo := s.activeModelRepo(); repo != "" {
		activeDir = strings.ReplaceAll(repo, "/", "__")
	}
	if activeDir == "" {
		return c, []string{"active embedding model is unknown — no cached model was classified as unused"}
	}

	repos, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, []string{fmt.Sprintf("could not read %s: %v", cacheDir, err)}
	}
	cutoff := s.now().Add(-ggufSettleWindow)
	for _, repo := range repos {
		if ctxDone(ctx) {
			break
		}
		if !repo.IsDir() || repo.Name() == activeDir {
			continue
		}
		repoDir := filepath.Join(cacheDir, repo.Name())
		files, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}
		names := map[string]struct{}{}
		for _, f := range files {
			names[f.Name()] = struct{}{}
		}
		for _, f := range files {
			if f.IsDir() || !strings.EqualFold(filepath.Ext(f.Name()), ".gguf") {
				continue
			}
			if _, partial := names[f.Name()+".partial"]; partial {
				continue
			}
			info, err := f.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			c.Items = append(c.Items, Item{
				Key:       filepath.Join(repo.Name(), f.Name()),
				Label:     strings.Replace(repo.Name(), "__", "/", 1),
				Detail:    f.Name(),
				SizeBytes: info.Size(),
				path:      filepath.Join(repoDir, f.Name()),
			})
		}
	}
	return c, nil
}
