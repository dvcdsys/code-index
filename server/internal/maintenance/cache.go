package maintenance

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// analysisTTL is how long an analysis_id stays redeemable by Clean. It is not
// a response cache: Analyze always recomputes (the admin pressed the button
// because they want fresh numbers). It bounds how stale the set described by a
// confirmation dialog is allowed to be before we make them look again.
const analysisTTL = 10 * time.Minute

// analysisCache holds exactly one analysis — the most recent.
//
// One slot rather than a map by id: there is a single admin surface and a
// single meaningful "latest analysis", while a map would grow without bound
// and let two admins clean against two different pictures of the same server.
type analysisCache struct {
	mu      sync.Mutex
	current *Analysis
	// running is non-nil while a scan is in flight and is closed when it
	// finishes. It collapses concurrent Analyze calls — React StrictMode's
	// double effects, an impatient double click and two open dashboards are
	// all realistic, and each duplicate would walk hundreds of thousands of
	// files for a result we are about to compute anyway.
	running chan struct{}
	ttl     time.Duration
	now     func() time.Time
}

func newAnalysisCache(ttl time.Duration, now func() time.Time) *analysisCache {
	if now == nil {
		now = time.Now
	}
	return &analysisCache{ttl: ttl, now: now}
}

// take returns the cached analysis if id matches and it has not expired, and
// removes it in the same critical section.
//
// Consuming under the read lock rather than reading now and invalidating later
// is what makes an analysis single-use: two concurrent Clean calls carrying the
// same id would otherwise both pass the lookup and both run. The deletions
// themselves are idempotent, so nothing would be destroyed twice — but the
// second caller would report bytes the first one had already reclaimed, and a
// number that double-counts is worse than an error.
func (c *analysisCache) take(id string) (*Analysis, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil || c.current.ID != id {
		return nil, false
	}
	if !c.now().Before(c.current.ExpiresAt) {
		c.current = nil
		return nil, false
	}
	a := c.current
	c.current = nil
	return a, true
}

// fresh reports whether a is non-nil and still inside its TTL.
func (c *analysisCache) fresh(a *Analysis) bool {
	if a == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now().Before(a.ExpiresAt)
}

// put installs a freshly computed analysis.
func (c *analysisCache) put(a *Analysis) {
	c.mu.Lock()
	c.current = a
	c.mu.Unlock()
}

// invalidate drops the cached analysis. Called after every Clean, successful
// or not: once anything has been deleted the numbers are wrong by
// construction, and showing pre-clean figures next to a "cleaned" toast is a
// lie the admin has no way to detect.
func (c *analysisCache) invalidate() {
	c.mu.Lock()
	c.current = nil
	c.mu.Unlock()
}

// beginScan claims the right to run a scan. It returns wait != nil when
// another scan is already in flight; the caller should block on it and then
// read the cache instead of scanning. When it returns done != nil the caller
// owns the scan and must call done() when finished.
func (c *analysisCache) beginScan() (done func(), wait <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running != nil {
		return nil, c.running
	}
	ch := make(chan struct{})
	c.running = ch
	return func() {
		c.mu.Lock()
		c.running = nil
		c.mu.Unlock()
		close(ch)
	}, nil
}

// latest returns whatever is cached, without checking an id. Used by the
// single-flight follower path, which must then check freshness itself.
func (c *analysisCache) latest() *Analysis {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// latestID is the id of whatever is cached, or "" when nothing is. Followers
// snapshot this before waiting so they can tell a new result from an old one.
func (c *analysisCache) latestID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return ""
	}
	return c.current.ID
}

// usageTTL is how long a usage snapshot is served before being recomputed.
// Short enough that the heap figure still tracks reality, long enough that a
// dashboard refresh does not re-walk hundreds of thousands of files.
const usageTTL = 15 * time.Second

// usageCache is the same single-flight-plus-TTL shape as analysisCache, for the
// far cheaper (but still walk-bound) usage snapshot.
type usageCache struct {
	mu      sync.Mutex
	current *Usage
	at      time.Time
	running chan struct{}
}

func (c *usageCache) get(now time.Time) (Usage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil || now.Sub(c.at) >= usageTTL {
		return Usage{}, false
	}
	return *c.current, true
}

func (c *usageCache) put(u Usage, now time.Time) {
	c.mu.Lock()
	c.current, c.at = &u, now
	c.mu.Unlock()
}

// begin claims the right to compute, mirroring analysisCache.beginScan.
func (c *usageCache) begin() (done func(), wait <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running != nil {
		return nil, c.running
	}
	ch := make(chan struct{})
	c.running = ch
	return func() {
		c.mu.Lock()
		c.running = nil
		c.mu.Unlock()
		close(ch)
	}, nil
}

// newAnalysisID returns an opaque token. Not a secret and not authorisation —
// every endpoint that accepts it is already admin-gated — just an identity for
// "the picture the admin was looking at".
func newAnalysisID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is fatal for TLS and sessions long before it
		// matters here; a fixed token still behaves correctly because the
		// cache holds one analysis at a time.
		return "analysis"
	}
	return hex.EncodeToString(b[:])
}
