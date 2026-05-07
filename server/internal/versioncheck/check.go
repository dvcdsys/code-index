// Package versioncheck periodically polls GitHub for the latest cix-server
// release tag and exposes the result for the dashboard "update available"
// banner. One poll per server, regardless of how many clients are open —
// the per-instance cache avoids hammering the GitHub API and keeps an
// untrusted browser from being able to forge "latest version" claims.
//
// Tag stream is hardcoded to `server/v*` (cli/v* lives on a separate stream
// per CLAUDE.md). Pre-releases and drafts are skipped. ETag-based revalidation
// keeps unauthenticated rate-limit usage at ~4 req/day per server (default
// 6h interval, vs. the 60/h GitHub limit).
package versioncheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sethvargo/go-retry"
)

// Config configures the version-check service. Zero-value fields fall back
// to documented defaults inside New().
type Config struct {
	Enabled        bool
	Interval       time.Duration
	InitialDelay   time.Duration
	BaseURL        string // default "https://api.github.com" — overridable for tests
	Repo           string // e.g. "dvcdsys/code-index"
	TagPrefix      string // default "server/v"
	CurrentVersion string // e.g. "0.5.1" or "0.0.0-dev"
	HTTPTimeout    time.Duration
	UserAgent      string

	// BackoffInitial — first retry delay after a failure. Default 1m.
	// Successive failures double the delay (with ±10s jitter) up to BackoffMax.
	// A successful poll resets the streak and the next attempt waits the
	// regular Interval. Set to 0 to disable adaptive backoff entirely (the
	// next attempt then waits the full Interval — pre-backoff behaviour).
	BackoffInitial time.Duration
	// BackoffMax — cap on the per-attempt delay during a failure streak.
	// Default 30m. Should stay <= Interval/2 so the regular schedule still
	// dominates once GitHub recovers.
	BackoffMax time.Duration
	// MaxBackoffAttempts — stop retrying after this many consecutive
	// failures and resume the regular Interval schedule, anchored to the
	// FIRST attempt of the streak (so a streak that exhausts at T+15m
	// schedules the next try at T0+Interval, not "now"+Interval). Default 5.
	MaxBackoffAttempts int
}

// Snapshot is a point-in-time view of the cached version-check state.
// All time / string fields are zero-valued when no successful check has
// happened yet — callers branch on LatestVersion != "" or CheckedAt.IsZero().
type Snapshot struct {
	Enabled         bool
	CurrentVersion  string
	LatestVersion   string
	UpdateAvailable bool
	ReleaseURL      string
	CheckedAt       time.Time
	LastError       string
}

// Service polls GitHub on a ticker and serves the cached result via Latest().
// Safe for concurrent use; Run blocks until the supplied context is canceled.
type Service struct {
	cfg    Config
	logger *slog.Logger
	client *http.Client

	mu        sync.RWMutex
	snapshot  Snapshot
	etag      string
	lastNotes string // unused for now; reserved for release-notes preview
}

// New builds a Service. Defaults: BaseURL=api.github.com, TagPrefix=server/v,
// HTTPTimeout=10s, UserAgent="cix-server/<CurrentVersion>".
func New(cfg Config, logger *slog.Logger) *Service {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.github.com"
	}
	if cfg.TagPrefix == "" {
		cfg.TagPrefix = "server/v"
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 10 * time.Second
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "cix-server/" + cfg.CurrentVersion
	}
	if cfg.BackoffInitial <= 0 {
		cfg.BackoffInitial = time.Minute
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 30 * time.Minute
	}
	if cfg.BackoffMax < cfg.BackoffInitial {
		cfg.BackoffMax = cfg.BackoffInitial
	}
	if cfg.MaxBackoffAttempts <= 0 {
		cfg.MaxBackoffAttempts = 5
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		cfg:    cfg,
		logger: logger,
		client: &http.Client{Timeout: cfg.HTTPTimeout},
		snapshot: Snapshot{
			Enabled:        cfg.Enabled,
			CurrentVersion: cfg.CurrentVersion,
		},
	}
}

// Latest returns a copy of the current snapshot. Cheap; safe for hot paths.
func (s *Service) Latest() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

// Run polls in a loop, sleeping Interval between successes and applying an
// exponential backoff (BackoffInitial → BackoffMax with jitter) during a
// failure streak. Returns immediately when the feature is disabled. Exits
// cleanly on ctx.Done().
func (s *Service) Run(ctx context.Context) {
	if !s.cfg.Enabled {
		s.logger.Info("version check disabled (CIX_VERSION_CHECK_ENABLED=false)")
		return
	}
	if s.cfg.Repo == "" {
		s.logger.Warn("version check enabled but CIX_VERSION_CHECK_REPO is empty — disabling")
		return
	}

	if s.cfg.InitialDelay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.cfg.InitialDelay):
		}
	}

	// newBackoff returns a fresh exponential schedule. Re-created when a
	// failure streak begins so the first retry uses BackoffInitial again
	// after each successful poll.
	newBackoff := func() retry.Backoff {
		// Jitter is min(10s, BackoffInitial/2) so tests with sub-second
		// initial delays don't drown the schedule in noise.
		jitter := 10 * time.Second
		if half := s.cfg.BackoffInitial / 2; half < jitter {
			jitter = half
		}
		b := retry.NewExponential(s.cfg.BackoffInitial)
		b = retry.WithJitter(jitter, b)
		b = retry.WithCappedDuration(s.cfg.BackoffMax, b)
		return b
	}

	var backoff retry.Backoff // nil = no active failure streak
	var streakStart time.Time
	var attempts int

	for {
		attemptStart := time.Now()
		err := s.CheckNow(ctx)

		var wait time.Duration
		if err != nil {
			if s.cfg.Interval <= 0 {
				return // one-shot mode (tests)
			}
			if backoff == nil {
				backoff = newBackoff()
				streakStart = attemptStart
				attempts = 0
			}
			attempts++

			if attempts >= s.cfg.MaxBackoffAttempts {
				// Streak exhausted — anchor the next attempt to the FIRST
				// failure's timestamp + Interval. Keeps the long-term
				// schedule on its 6h grid even when a backoff streak ate
				// up to ~30m of it. If the streak somehow ran longer than
				// Interval (degenerate config), fire immediately.
				elapsed := time.Since(streakStart)
				wait = s.cfg.Interval - elapsed
				if wait < 0 {
					wait = 0
				}
				s.logger.Warn("version check streak exhausted — anchoring to schedule",
					"err", err,
					"attempts", attempts,
					"next_attempt_in", wait.String())
				backoff = nil
			} else {
				next, _ := backoff.Next()
				wait = next
				s.logger.Warn("version check failed — backing off",
					"err", err,
					"attempt", attempts,
					"next_attempt_in", wait.String())
			}
		} else {
			backoff = nil // reset streak
			if s.cfg.Interval <= 0 {
				return
			}
			wait = s.cfg.Interval
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// CheckNow performs a single GitHub poll and updates the cache.
// Returns the error so callers (including Run) can log it; the cache
// already records the error in LastError on failure.
func (s *Service) CheckNow(ctx context.Context) error {
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=30", strings.TrimRight(s.cfg.BaseURL, "/"), s.cfg.Repo)
	reqCtx, cancel := context.WithTimeout(ctx, s.cfg.HTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		s.recordError(fmt.Errorf("build request: %w", err))
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", s.cfg.UserAgent)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	s.mu.RLock()
	if s.etag != "" {
		req.Header.Set("If-None-Match", s.etag)
	}
	s.mu.RUnlock()

	resp, err := s.client.Do(req)
	if err != nil {
		s.recordError(fmt.Errorf("github request: %w", err))
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		// Cached snapshot still valid. Bump CheckedAt and clear any
		// previous error so callers see "we tried, all good" rather
		// than a stale failure.
		s.mu.Lock()
		s.snapshot.CheckedAt = time.Now().UTC()
		s.snapshot.LastError = ""
		s.mu.Unlock()
		return nil
	case http.StatusOK:
		// fall through
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		err := fmt.Errorf("github status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		s.recordError(err)
		return err
	}

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		s.recordError(fmt.Errorf("decode releases: %w", err))
		return err
	}

	latestTag, htmlURL := pickLatest(releases, s.cfg.TagPrefix)
	if latestTag == "" {
		s.recordError(fmt.Errorf("no releases matching prefix %q", s.cfg.TagPrefix))
		return nil
	}

	etag := resp.Header.Get("ETag")
	updateAvail := isNewer(s.cfg.CurrentVersion, latestTag)

	s.mu.Lock()
	s.etag = etag
	s.snapshot = Snapshot{
		Enabled:         true,
		CurrentVersion:  s.cfg.CurrentVersion,
		LatestVersion:   latestTag,
		UpdateAvailable: updateAvail,
		ReleaseURL:      htmlURL,
		CheckedAt:       time.Now().UTC(),
		LastError:       "",
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) recordError(err error) {
	s.mu.Lock()
	s.snapshot.CheckedAt = time.Now().UTC()
	s.snapshot.LastError = err.Error()
	s.mu.Unlock()
}

// githubRelease is the subset of the GitHub Release schema we care about.
// Full schema: https://docs.github.com/en/rest/releases/releases
type githubRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// pickLatest filters releases by tag prefix, drops drafts/prereleases, and
// returns the highest-semver tag (with prefix stripped) plus its release URL.
// Empty result means no matching release.
func pickLatest(rels []githubRelease, prefix string) (tag, url string) {
	var bestTag, bestURL string
	for _, r := range rels {
		if r.Draft || r.Prerelease {
			continue
		}
		if !strings.HasPrefix(r.TagName, prefix) {
			continue
		}
		v := strings.TrimPrefix(r.TagName, prefix)
		// Defensive: drop anything that still looks like a prerelease
		// (`-rc1`, `-beta`) even if GitHub didn't flag it.
		if strings.ContainsAny(v, "-+") {
			continue
		}
		if bestTag == "" || compareSemver(v, bestTag) > 0 {
			bestTag, bestURL = v, r.HTMLURL
		}
	}
	return bestTag, bestURL
}

// isNewer reports whether `latest` is a strictly newer semver than `current`.
// Treats unparseable / dev / empty current as "anything is newer".
func isNewer(current, latest string) bool {
	if latest == "" {
		return false
	}
	cur := strings.TrimPrefix(current, "v")
	if cur == "" || strings.Contains(cur, "-dev") || !looksNumeric(cur) {
		// dev / unknown build → always offer the upgrade
		return true
	}
	return compareSemver(latest, cur) > 0
}

// compareSemver compares two `MAJOR.MINOR.PATCH` strings numerically.
// Returns -1, 0, 1. Non-numeric components compare lexicographically as a
// safety fallback (shouldn't happen given pickLatest's filter).
func compareSemver(a, b string) int {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		ai, aOK := 0, true
		bi, bOK := 0, true
		if i < len(pa) {
			ai, aOK = atoi(pa[i])
		}
		if i < len(pb) {
			bi, bOK = atoi(pb[i])
		}
		if aOK && bOK {
			if ai != bi {
				if ai < bi {
					return -1
				}
				return 1
			}
			continue
		}
		// Lexicographic fallback when at least one component is non-numeric.
		var as, bs string
		if i < len(pa) {
			as = pa[i]
		}
		if i < len(pb) {
			bs = pb[i]
		}
		if as != bs {
			if as < bs {
				return -1
			}
			return 1
		}
	}
	return 0
}

func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func looksNumeric(v string) bool {
	for _, p := range strings.Split(v, ".") {
		if _, ok := atoi(p); !ok {
			return false
		}
	}
	return true
}
