package httpapi

import (
	"net/http/httptest"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/versioncheck"
)

// The goldens below pin the exact wire JSON of GET /api/v1/status. The CLI and
// the macOS launcher live in a SEPARATE module (github.com/dvcdsys/code-index/
// cli) that cannot import this one, so the contract is held by twin fixtures:
// identical goldens live in cli/internal/client/status_test.go. Rename, retype
// or drop a field on either side and one of these tests fails, forcing the
// other module to be updated in the same PR.
//
// Key order here is alphabetical because GetStatus builds a map[string]any and
// encoding/json sorts map keys. That is stable, so the bytes can be pinned.
//
// Two goldens, because the response has two shapes. The version-check fields
// are folded in only when Deps.VersionCheck is wired; a consumer that assumes
// they are always present will read `update_available: false` as "you are up to
// date" on a server where the check is simply switched off.
const (
	wantStatusWire = `{"active_indexing_jobs":0,"api_version":"v1","backend":"go","embedding_model":"awhiteside/CodeRankEmbed-Q8_0-GGUF","embedding_provider":"","embedding_provider_manages_process":false,"model_loaded":false,"projects":0,"server_version":"1.2.3","status":"ok"}`

	wantStatusWithVersionCheckWire = `{"active_indexing_jobs":0,"api_version":"v1","backend":"go","embedding_model":"awhiteside/CodeRankEmbed-Q8_0-GGUF","embedding_provider":"","embedding_provider_manages_process":false,"latest_version":null,"model_loaded":false,"projects":0,"release_url":null,"server_version":"1.2.3","status":"ok","update_available":false,"version_check":{"checked_at":null,"enabled":true,"error":null}}`
)

func statusServer(vc *versioncheck.Service) *Server {
	// Deliberately minimal Deps: a nil DB makes the two counts 0 and a nil
	// EmbeddingSvc makes the provider fields their zero values, so the body is
	// deterministic without standing up a database or a llama sidecar.
	return &Server{Deps: Deps{
		ServerVersion:  "1.2.3",
		APIVersion:     "v1",
		Backend:        "go",
		EmbeddingModel: "awhiteside/CodeRankEmbed-Q8_0-GGUF",
		VersionCheck:   vc,
	}}
}

func TestGetStatus_WireContract(t *testing.T) {
	rec := httptest.NewRecorder()
	statusServer(nil).GetStatus(rec, httptest.NewRequest("GET", "/api/v1/status", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := trimTrailingNewline(rec.Body.String())
	if got != wantStatusWire {
		t.Errorf("/status wire shape drifted from the CLI contract:\n got: %s\nwant: %s\n"+
			"→ update cli/internal/client/client.go and its twin fixture in the same PR.", got, wantStatusWire)
	}
}

func TestGetStatus_WireContract_WithVersionCheck(t *testing.T) {
	// Never Run(), so the snapshot is the constructor's initial state: enabled,
	// nothing checked yet. That is exactly the shape a client sees during the
	// first minute of every server's life (the poller has a 60s initial delay),
	// so it is worth pinning rather than only the post-check shape.
	vc := versioncheck.New(versioncheck.Config{
		Enabled:        true,
		Repo:           "dvcdsys/code-index",
		CurrentVersion: "1.2.3",
	}, nil)

	rec := httptest.NewRecorder()
	statusServer(vc).GetStatus(rec, httptest.NewRequest("GET", "/api/v1/status", nil))

	got := trimTrailingNewline(rec.Body.String())
	if got != wantStatusWithVersionCheckWire {
		t.Errorf("/status version-check shape drifted from the CLI contract:\n got: %s\nwant: %s\n"+
			"→ update cli/internal/client/client.go and its twin fixture in the same PR.", got, wantStatusWithVersionCheckWire)
	}
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
