package embeddings

import (
	"encoding/json"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/config"
	"github.com/dvcdsys/code-index/server/internal/embeddings/provider/ollama"
)

// The persisted ollama blob froze two values at first boot that describe the
// installation rather than the user's choices. Re-deriving them is what keeps
// an upgraded or relocated install pointing at the llama-server it actually
// shipped with — and what stops a second server adopting an orphaned sidecar
// through a recycled socket name.
func TestRefreshOllamaSidecarPaths(t *testing.T) {
	stored, err := json.Marshal(ollama.Config{
		Model:       "awhiteside/CodeRankEmbed-Q8_0-GGUF",
		BinDir:      "/Users/someone/.cix/runtime/0.5.0/llama",
		SocketPath:  "/tmp/cix-llama-4242.sock",
		Transport:   "unix",
		CacheDir:    "/Users/someone/Library/Caches/cix/models",
		CtxSize:     4096,
		NGpuLayers:  -1,
		NThreads:    7,
		BatchSize:   2048,
		CacheRAMMiB: 0,
		StartupSec:  300,
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		LlamaBinDir:     "/Users/someone/.cix/runtime/0.6.0/llama",
		LlamaSocketPath: "/tmp/cix-llama-9001.sock",
	}

	out, err := RefreshOllamaSidecarPaths(cfg, stored)
	if err != nil {
		t.Fatal(err)
	}

	var got ollama.Config
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	if got.BinDir != cfg.LlamaBinDir {
		t.Errorf("bin_dir = %q, want %q", got.BinDir, cfg.LlamaBinDir)
	}
	if got.SocketPath != cfg.LlamaSocketPath {
		t.Errorf("socket_path = %q, want %q", got.SocketPath, cfg.LlamaSocketPath)
	}

	// Everything a person can choose has to survive untouched. Overwriting the
	// model would silently change the embedding identity and force a reindex of
	// every project; overwriting the tuning would discard dashboard edits on
	// every boot.
	if got.Model != "awhiteside/CodeRankEmbed-Q8_0-GGUF" {
		t.Errorf("model = %q, want it unchanged", got.Model)
	}
	if got.CtxSize != 4096 || got.NGpuLayers != -1 || got.NThreads != 7 || got.BatchSize != 2048 {
		t.Errorf("tuning changed: %+v", got)
	}
	if got.Transport != "unix" || got.CacheDir != "/Users/someone/Library/Caches/cix/models" {
		t.Errorf("transport/cache_dir changed: transport=%q cache_dir=%q", got.Transport, got.CacheDir)
	}
	if got.StartupSec != 300 {
		t.Errorf("startup_sec = %d, want 300", got.StartupSec)
	}
}

func TestRefreshOllamaSidecarPathsRejectsMalformedBlob(t *testing.T) {
	if _, err := RefreshOllamaSidecarPaths(&config.Config{}, []byte("not json")); err == nil {
		t.Fatal("RefreshOllamaSidecarPaths accepted a malformed blob")
	}
}
