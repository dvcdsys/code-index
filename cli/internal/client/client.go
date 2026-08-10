package client

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client

	// customHeaders are user-configured headers attached to every outbound
	// request (in addition to the cix Bearer) so the client can satisfy an
	// authenticating reverse proxy in front of cix. Values are already
	// ${ENV}-expanded and validated by the caller (getClient). Never logged.
	customHeaders map[string]string

	// streamingClient is used for endpoints that return chunked NDJSON
	// (currently only POST /index/files when Accept advertises x-ndjson).
	// Timeout is 0 because the natural duration of an indexing batch is
	// dominated by GPU embed time and there is no useful overall ceiling.
	// Idle silence is bounded by streamingIdleTimeout instead.
	streamingClient      *http.Client
	streamingIdleTimeout time.Duration
}

// defaultStreamingIdleTimeout is the maximum allowed gap between events on a
// streaming response. Server emits a heartbeat every 10s, so 60s gives a 6×
// margin — enough to absorb a one-shot llama-supervisor restart (which can
// pause embedding for ~5s several times in a row before the queue catches up)
// or a network hiccup, without giving up on a still-progressing batch.
const defaultStreamingIdleTimeout = 60 * time.Second

// New creates a new API client
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 600 * time.Second,
		},
		streamingClient:      &http.Client{Timeout: 0},
		streamingIdleTimeout: defaultStreamingIdleTimeout,
	}
}

// SetCustomHeaders configures extra headers attached to every request. The
// map is used as-is (values must already be expanded/validated by the caller).
// Passing nil or an empty map is a no-op — current behavior is preserved.
func (c *Client) SetCustomHeaders(h map[string]string) {
	c.customHeaders = h
}

// applyCustomHeaders attaches the configured custom headers to req. It is
// always called BEFORE the cix-managed headers (Authorization, Content-Type,
// Accept) are set, so a stray config value can never clobber authentication.
func (c *Client) applyCustomHeaders(req *http.Request) {
	for k, v := range c.customHeaders {
		req.Header.Set(k, v)
	}
}

// SetStreamingIdleTimeout overrides the silence threshold for streaming
// endpoints. Pass 0 to disable the watchdog entirely (not recommended).
func (c *Client) SetStreamingIdleTimeout(d time.Duration) {
	c.streamingIdleTimeout = d
}

// BaseURL returns the base URL this client is configured to use.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// do performs an HTTP request with auth
func (c *Client) do(method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonData)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Custom headers first, then cix-managed headers — so the latter win.
	c.applyCustomHeaders(req)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}

	return resp, nil
}

// encodeProjectPath returns the project's URL hash (first 16 hex chars of
// SHA1 of the identity key). MUST stay byte-identical to the server's
// projects.Create key derivation (server/internal/projects/projects.go).
//
// The server namespaces ONLY local projects, and only those: it wraps the
// identity key as "local:{machine_id}:{path}" when MachineID != "" (set during
// `cix init` for a real filesystem project), and uses the path as-is otherwise.
// External projects (GitHub repos attached via the dashboard) have no MachineID
// and a globally-unique host_path like "github.com/owner/repo@branch", which is
// hashed bare.
//
// We mirror that split with filepath.IsAbs: local projects are always addressed
// by an ABSOLUTE filesystem path (the CLI runs filepath.Abs on cwd / -p before
// it gets here), so an absolute input is local → namespace it. A non-absolute
// input is an external project identifier (e.g. from `cix search -n
// github.com/owner/repo@branch`) → hash it bare so the hash matches the row the
// server stored. Wrapping external IDs in "local:" was a regression that made
// every `-n <external>` lookup 404.
func encodeProjectPath(path string) string {
	key := path
	if filepath.IsAbs(path) {
		key = "local:" + machineID() + ":" + path
	}
	h := sha1.Sum([]byte(key))
	return fmt.Sprintf("%x", h)[:16]
}

// EncodeProjectPath is the exported project URL hash, for tests and tooling
// that need to mirror the client's addressing exactly.
func EncodeProjectPath(path string) string { return encodeProjectPath(path) }

var (
	machineIDOnce sync.Once
	machineIDVal  string
)

// machineID returns a stable per-machine (per-home) identifier, persisted at
// ~/.cix/machine_id. Generated on first use. Used to namespace local project
// identity so different developers' machines never collide on the same path.
func machineID() string {
	machineIDOnce.Do(func() { machineIDVal = loadOrCreateMachineID() })
	return machineIDVal
}

func loadOrCreateMachineID() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "unknown-machine"
	}
	path := filepath.Join(home, ".cix", "machine_id")
	if b, rerr := os.ReadFile(path); rerr == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id
		}
	}
	buf := make([]byte, 16)
	if _, gerr := rand.Read(buf); gerr != nil {
		return "unknown-machine"
	}
	id := hex.EncodeToString(buf)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(id+"\n"), 0o600)
	return id
}

// machineLabel returns the OS hostname for display purposes (sent to the
// server as machine_label). Best-effort; empty when unavailable.
func machineLabel() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return ""
}

// parseResponse reads and unmarshals JSON response
func parseResponse(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Detail != "" {
			return fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Detail)
		}
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	if v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}

	return nil
}

// Health checks if the API server is running. Although /health is public on
// cix, the request must still carry any custom headers — behind an
// authenticating reverse proxy the probe would otherwise be bounced (302/403)
// at the edge before reaching cix.
func (c *Client) Health() error {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	c.applyCustomHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: status %d", resp.StatusCode)
	}

	return nil
}

// Status gets the API server status
func (c *Client) Status() (*StatusResponse, error) {
	resp, err := c.do("GET", "/api/v1/status", nil)
	if err != nil {
		return nil, err
	}

	var status StatusResponse
	if err := parseResponse(resp, &status); err != nil {
		return nil, err
	}

	return &status, nil
}

// StatusResponse mirrors GET /api/v1/status.
//
// The first block is always present. The version-check block is present only
// when the server was started with the version-check service wired
// (CIX_VERSION_CHECK_ENABLED); when it is absent the four fields stay at their
// zero values, and `UpdateAvailable == false` is then "unknown", not "current".
// Branch on `VersionCheck != nil` before trusting it.
//
// LatestVersion and ReleaseURL are pointers because the server emits JSON null
// for them until a check has actually succeeded — an empty string would be
// indistinguishable from "checked, and there is no newer release".
//
// The exact wire shape is pinned by a twin golden fixture: status_test.go here
// and server/internal/httpapi/status_contract_test.go there. The two modules
// cannot import each other, so those fixtures are the contract — change one and
// the other fails, forcing both sides to move in the same PR.
type StatusResponse struct {
	Status                          string `json:"status"`
	Backend                         string `json:"backend"`
	ServerVersion                   string `json:"server_version"`
	APIVersion                      string `json:"api_version"`
	ModelLoaded                     bool   `json:"model_loaded"`
	EmbeddingModel                  string `json:"embedding_model"`
	EmbeddingProvider               string `json:"embedding_provider"`
	EmbeddingProviderManagesProcess bool   `json:"embedding_provider_manages_process"`
	Projects                        int    `json:"projects"`
	ActiveIndexingJobs              int    `json:"active_indexing_jobs"`

	UpdateAvailable bool                `json:"update_available"`
	LatestVersion   *string             `json:"latest_version"`
	ReleaseURL      *string             `json:"release_url"`
	VersionCheck    *VersionCheckStatus `json:"version_check"`
}

// VersionCheckStatus is the nested `version_check` object.
type VersionCheckStatus struct {
	Enabled bool    `json:"enabled"`
	Error   *string `json:"error"`
	// CheckedAt is RFC3339, or nil when no check has completed yet.
	CheckedAt *string `json:"checked_at"`
}

// EmbeddingsHealthy reports whether the embedding provider should be shown as
// working.
//
// ModelLoaded alone is not that answer. The server computes it under a 500 ms
// deadline, and for HTTP providers (openai, voyage) there is no local process
// whose liveness it could reflect — EmbeddingProviderManagesProcess is false
// there, and treating a slow or skipped probe as "down" would show a permanent
// red dot for a provider that is fine. Only a managed local process
// (llama-server) can meaningfully be reported as not loaded.
func (s *StatusResponse) EmbeddingsHealthy() bool {
	if !s.EmbeddingProviderManagesProcess {
		return true
	}
	return s.ModelLoaded
}
