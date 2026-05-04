package httpapi

// Login rate limiter — protects POST /api/v1/auth/login from brute-force
// credential attacks. Two sliding-window counters live in memory:
//
//   - per (IP, lower-cased email): keyLimit attempts within keyWindow.
//     Slows password guessing against a known account.
//   - per IP: ipLimit attempts within ipWindow. Slows horizontal sweeps
//     across many emails from a single source.
//
// On a successful login the per-(IP, email) counter is cleared so a user
// who fat-fingered their password a few times then succeeds is not stuck
// behind their own counter. The per-IP counter is intentionally NOT
// cleared — otherwise an attacker could mix one valid login into a
// horizontal sweep to lift the global cap.
//
// The implementation is a single mutex over two maps. At the rates we
// permit (~600/hr peak per IP) contention is irrelevant for an admin
// tool. State is in-process; restarts wipe the counters, which is fine —
// the attacker has to re-establish the connection state anyway.

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type loginLimiter struct {
	mu     sync.Mutex
	perKey map[string][]time.Time // "ip|lower(email)" → recent attempt timestamps
	perIP  map[string][]time.Time // ip → recent attempt timestamps

	keyLimit  int
	keyWindow time.Duration
	ipLimit   int
	ipWindow  time.Duration

	now func() time.Time // overridable in tests
}

// newLoginLimiter returns a limiter with the production defaults:
// 5 attempts / 15 min per (IP, email) and 60 attempts / minute per IP.
func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		perKey:    map[string][]time.Time{},
		perIP:     map[string][]time.Time{},
		keyLimit:  5,
		keyWindow: 15 * time.Minute,
		ipLimit:   60,
		ipWindow:  time.Minute,
		now:       time.Now,
	}
}

func loginLimiterKey(ip, email string) string {
	return ip + "|" + strings.ToLower(strings.TrimSpace(email))
}

// allow returns (true, 0) when the caller may proceed with authentication
// and records the attempt against both windows. Returns (false, retry)
// when either window is full; the caller must respond with 429 and
// `Retry-After: retry`.
func (l *loginLimiter) allow(ip, email string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	if pruned, retry, blocked := checkSlidingWindow(l.perIP[ip], now, l.ipWindow, l.ipLimit); blocked {
		l.perIP[ip] = pruned
		return false, retry
	}

	key := loginLimiterKey(ip, email)
	if pruned, retry, blocked := checkSlidingWindow(l.perKey[key], now, l.keyWindow, l.keyLimit); blocked {
		l.perKey[key] = pruned
		return false, retry
	}

	l.perIP[ip] = append(pruneOlder(l.perIP[ip], now.Add(-l.ipWindow)), now)
	l.perKey[key] = append(pruneOlder(l.perKey[key], now.Add(-l.keyWindow)), now)
	return true, 0
}

// reset clears the per-(IP, email) counter after a successful login. The
// per-IP counter is left in place by design — see file-level comment.
func (l *loginLimiter) reset(ip, email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.perKey, loginLimiterKey(ip, email))
}

func checkSlidingWindow(ts []time.Time, now time.Time, window time.Duration, limit int) ([]time.Time, time.Duration, bool) {
	pruned := pruneOlder(ts, now.Add(-window))
	if len(pruned) >= limit {
		retry := max(window-now.Sub(pruned[0]), time.Second)
		return pruned, retry, true
	}
	return pruned, 0, false
}

func pruneOlder(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	if i == 0 {
		return ts
	}
	return append(ts[:0:0], ts[i:]...)
}

// writeRateLimited emits a 429 response with the Retry-After header set
// to the number of seconds until at least one slot frees up.
func writeRateLimited(w http.ResponseWriter, retry time.Duration) {
	secs := max(int(retry.Seconds()), 1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "Too many login attempts; try again later.")
}
