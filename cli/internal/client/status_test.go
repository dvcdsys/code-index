package client

import (
	"encoding/json"
	"testing"
)

// Twin of server/internal/httpapi/status_contract_test.go. The two modules
// cannot import each other, so these byte-identical goldens are the contract
// for GET /api/v1/status: change a field name or type on either side and one of
// the two test files fails, forcing both to move in the same PR.
//
// Key order is alphabetical because the server builds the response as a
// map[string]any and encoding/json sorts map keys. Order does not matter for
// decoding — it is kept identical to make the twin obvious on inspection.
const (
	wantStatusWire = `{"active_indexing_jobs":0,"api_version":"v1","backend":"go","embedding_model":"awhiteside/CodeRankEmbed-Q8_0-GGUF","embedding_provider":"","embedding_provider_manages_process":false,"model_loaded":false,"projects":0,"server_version":"1.2.3","status":"ok"}`

	wantStatusWithVersionCheckWire = `{"active_indexing_jobs":0,"api_version":"v1","backend":"go","embedding_model":"awhiteside/CodeRankEmbed-Q8_0-GGUF","embedding_provider":"","embedding_provider_manages_process":false,"latest_version":null,"model_loaded":false,"projects":0,"release_url":null,"server_version":"1.2.3","status":"ok","update_available":false,"version_check":{"checked_at":null,"enabled":true,"error":null}}`
)

func TestStatusResponse_DecodesServerContract(t *testing.T) {
	var got StatusResponse
	if err := json.Unmarshal([]byte(wantStatusWire), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := StatusResponse{
		Status:         "ok",
		Backend:        "go",
		ServerVersion:  "1.2.3",
		APIVersion:     "v1",
		EmbeddingModel: "awhiteside/CodeRankEmbed-Q8_0-GGUF",
	}
	if got != want {
		t.Errorf("decoded status drifted from the server contract:\n got: %+v\nwant: %+v\n"+
			"→ update server/internal/httpapi/server.go and its twin fixture in the same PR.", got, want)
	}

	// A server with the version check switched off sends none of these. The
	// nil pointers are what tells a caller "unknown" apart from "up to date".
	if got.VersionCheck != nil {
		t.Errorf("VersionCheck = %+v, want nil when the server omits the block", got.VersionCheck)
	}
}

func TestStatusResponse_DecodesVersionCheckBlock(t *testing.T) {
	var got StatusResponse
	if err := json.Unmarshal([]byte(wantStatusWithVersionCheckWire), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.VersionCheck == nil {
		t.Fatal("VersionCheck = nil, want the decoded block")
	}
	if !got.VersionCheck.Enabled {
		t.Error("VersionCheck.Enabled = false, want true")
	}
	// Before the first successful poll the server sends nulls, not empty
	// strings — that distinction is the whole reason these are pointers.
	if got.VersionCheck.CheckedAt != nil {
		t.Errorf("VersionCheck.CheckedAt = %q, want nil before the first check", *got.VersionCheck.CheckedAt)
	}
	if got.VersionCheck.Error != nil {
		t.Errorf("VersionCheck.Error = %q, want nil", *got.VersionCheck.Error)
	}
	if got.LatestVersion != nil || got.ReleaseURL != nil {
		t.Errorf("LatestVersion/ReleaseURL = %v/%v, want nil before the first check", got.LatestVersion, got.ReleaseURL)
	}
	if got.UpdateAvailable {
		t.Error("UpdateAvailable = true, want false")
	}
}

func TestEmbeddingsHealthy(t *testing.T) {
	tests := []struct {
		name           string
		managesProcess bool
		modelLoaded    bool
		want           bool
	}{
		// llama-server: the only case where "not loaded" is real information.
		{"managed process, loaded", true, true, true},
		{"managed process, not loaded", true, false, false},
		// openai / voyage: there is no local process to be down, and the
		// server's Ready() probe runs under a 500 ms deadline it can lose.
		// Reporting red here would mean a permanently red dot on a provider
		// that works — which is exactly what the dashboard avoids doing.
		{"http provider, loaded", false, true, true},
		{"http provider, not loaded", false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &StatusResponse{
				EmbeddingProviderManagesProcess: tc.managesProcess,
				ModelLoaded:                     tc.modelLoaded,
			}
			if got := s.EmbeddingsHealthy(); got != tc.want {
				t.Errorf("EmbeddingsHealthy() = %v, want %v", got, tc.want)
			}
		})
	}
}
