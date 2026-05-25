// Package tunnels orchestrates an outbound "managed tunnel" that gives the
// cix-server a publicly-reachable URL while it sits behind NAT. The tunnel
// is a general-purpose ingress; its first (currently only) consumer is
// GitHub webhook auto-registration (see reconciler.go), which prefers the
// live tunnel URL over CIX_PUBLIC_URL.
//
// The backend is provider-pluggable: Cloudflare Tunnel (cloudflared) ships
// now; ngrok is reserved as a future provider. Everything lives in one
// package so the Manager can construct providers without an import cycle.
package tunnels

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Provider identifiers. Stored verbatim in config + surfaced in the API.
const (
	ProviderCloudflare = "cloudflare"
	ProviderNgrok      = "ngrok"
)

// ErrNoActiveTunnel is returned by Manager actions when no provider is
// running (tunnel disabled or failed to construct).
var ErrNoActiveTunnel = errors.New("no active tunnel")

// ErrBinManagementDisabled is returned by InstallBinary when
// CIX_TUNNEL_BIN_MANAGED is off (binaries come from the system).
var ErrBinManagementDisabled = errors.New("tunnel binary management is disabled")

// State is the coarse lifecycle state surfaced to the dashboard.
type State string

const (
	StateDisabled   State = "disabled"   // tunnel turned off
	StateConnecting State = "connecting" // process up, not yet ready / no URL
	StateLive       State = "live"       // connected, public URL available
	StateFailed     State = "failed"     // gave up after the restart budget
)

// Status is the dashboard-facing snapshot of a provider. Fields are scalar
// so the JSON body stays stable across versions.
type Status struct {
	Provider  string `json:"provider"`
	State     State  `json:"state"`
	Mode      string `json:"mode,omitempty"`       // "named" | "quick"
	PublicURL string `json:"public_url,omitempty"` // base URL, no trailing slash
	PID       int    `json:"pid,omitempty"`
	UptimeSec int64  `json:"uptime_sec,omitempty"`
	LastError string `json:"last_error,omitempty"`
	Available bool   `json:"available"`      // false for not-yet-implemented providers
	Note      string `json:"note,omitempty"` // e.g. "coming soon"
}

// TestResult is the outcome of an end-to-end connectivity probe: a GET to
// the tunnel's public URL + "/health" that should egress through the tunnel
// provider and arrive back at this server.
type TestResult struct {
	OK         bool   `json:"ok"`
	PublicURL  string `json:"public_url,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	LatencyMS  int64  `json:"latency_ms,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// Provider is the contract every tunnel backend implements. A provider owns
// one external subprocess and its lifecycle.
type Provider interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	Status() Status
	URL() string // active public base URL, "" when down / not yet known
	TestConnection(ctx context.Context) (TestResult, error)
	// OnURLChange registers a callback fired whenever the public URL becomes
	// known or changes (quick tunnels get a fresh URL on every restart). Used
	// by the Manager to trigger webhook reconciliation.
	OnURLChange(fn func(string))
}

func uptimeSeconds(started time.Time) int64 {
	if started.IsZero() {
		return 0
	}
	return int64(time.Since(started).Seconds())
}

// probePublicURL is the shared end-to-end connectivity check used by every
// provider's TestConnection: a GET to <url>/health that egresses to the
// provider edge and routes back through the tunnel to this server.
func probePublicURL(ctx context.Context, client *http.Client, url string) (TestResult, error) {
	if url == "" {
		return TestResult{OK: false, Detail: "tunnel has no public URL yet"}, nil
	}
	target := strings.TrimRight(url, "/") + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return TestResult{OK: false, PublicURL: url, Detail: err.Error()}, nil
	}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return TestResult{OK: false, PublicURL: url, LatencyMS: latency.Milliseconds(), Detail: err.Error()}, nil
	}
	defer resp.Body.Close()
	ok := resp.StatusCode == http.StatusOK
	detail := "round-trip through the tunnel succeeded"
	if !ok {
		detail = fmt.Sprintf("tunnel reachable but /health returned %d", resp.StatusCode)
	}
	return TestResult{
		OK:         ok,
		PublicURL:  url,
		StatusCode: resp.StatusCode,
		LatencyMS:  latency.Milliseconds(),
		Detail:     detail,
	}, nil
}
