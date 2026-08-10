package main

import (
	"testing"

	"github.com/dvcdsys/code-index/cli/internal/client"
)

func TestProviderLabel(t *testing.T) {
	tests := map[string]string{
		// The bundled llama-server supervisor. Showing the raw kind here would
		// tell the user they are running Ollama, which is not installed and
		// never was — see the comment on providerLabel.
		"ollama": "llama.cpp (bundled)",
		"openai": "openai",
		"voyage": "voyage",
		"":       "unknown",
	}
	for kind, want := range tests {
		if got := providerLabel(kind); got != want {
			t.Errorf("providerLabel(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestSnapshotLines(t *testing.T) {
	running := snapshot{
		State:   stateRunning,
		Port:    21847,
		Managed: true,
		Status: &client.StatusResponse{
			EmbeddingProvider: "ollama",
			EmbeddingModel:    "ollama:awhiteside/CodeRankEmbed-Q8_0-GGUF",
		},
		EmbeddingsOK: true,
	}

	if got, want := running.ServerLine(), "cix-server: Running (:21847)"; got != want {
		t.Errorf("ServerLine() = %q, want %q", got, want)
	}
	if got, want := running.EmbeddingsLine(), "Embeddings: llama.cpp (bundled) — ready"; got != want {
		t.Errorf("EmbeddingsLine() = %q, want %q", got, want)
	}
	// The provider prefix is stripped: the row above already names the
	// provider, and repeating the misleading kind defeats providerLabel.
	if got, want := running.ModelLine(), "Model: awhiteside/CodeRankEmbed-Q8_0-GGUF"; got != want {
		t.Errorf("ModelLine() = %q, want %q", got, want)
	}
}

func TestSnapshotLines_NotRunning(t *testing.T) {
	// A cold start loads the embedding model in silence for up to a few
	// minutes. Calling that "Stopped" is what makes people kill and restart a
	// server that was nearly ready, so it gets its own state.
	starting := snapshot{State: stateStarting, Managed: true}
	if got, want := starting.ServerLine(), "cix-server: Starting…"; got != want {
		t.Errorf("ServerLine() = %q, want %q", got, want)
	}
	// Provider details from a server that is not answering are stale by
	// definition, so no row claims otherwise.
	if got, want := starting.ModelLine(), ""; got != want {
		t.Errorf("ModelLine() = %q, want %q (hidden)", got, want)
	}
	if got, want := starting.EmbeddingsLine(), "Embeddings: unknown"; got != want {
		t.Errorf("EmbeddingsLine() = %q, want %q", got, want)
	}

	// An agent installed by install-server.sh owns the same launchd label. The
	// app observes it but must not offer to drive it.
	external := snapshot{State: stateStopped, Managed: false}
	if got, want := external.ServerLine(), "cix-server: Stopped (managed externally)"; got != want {
		t.Errorf("ServerLine() = %q, want %q", got, want)
	}
}

func TestPollerDebouncesNotReady(t *testing.T) {
	// model_loaded is computed server-side under a 500 ms deadline it can
	// legitimately lose under load. One false is not evidence; flipping the
	// menu row on it produces a flicker while nothing is wrong.
	p := newPoller()
	p.snap.EmbeddingsOK = true

	notReady := &client.StatusResponse{
		EmbeddingProvider:               "ollama",
		EmbeddingProviderManagesProcess: true,
		ModelLoaded:                     false,
	}

	p.applyStatus(notReady)
	if !p.snapshotNow().EmbeddingsOK {
		t.Error("EmbeddingsOK flipped after a single not-ready poll; want it held until the second")
	}

	p.applyStatus(notReady)
	if p.snapshotNow().EmbeddingsOK {
		t.Error("EmbeddingsOK still true after two consecutive not-ready polls")
	}

	// One good poll clears the count, so a later single miss cannot flip it.
	p.applyStatus(&client.StatusResponse{
		EmbeddingProvider:               "ollama",
		EmbeddingProviderManagesProcess: true,
		ModelLoaded:                     true,
	})
	if !p.snapshotNow().EmbeddingsOK {
		t.Fatal("EmbeddingsOK false after a ready poll")
	}
	p.applyStatus(notReady)
	if !p.snapshotNow().EmbeddingsOK {
		t.Error("a single not-ready poll after a ready one flipped the row")
	}
}

func TestPollerTrustsHTTPProviders(t *testing.T) {
	// openai / voyage have no local process to be down, and the server skips
	// the liveness probe for them. Debouncing to "not ready" there would show a
	// permanently red row for a provider that works.
	p := newPoller()
	http := &client.StatusResponse{
		EmbeddingProvider:               "voyage",
		EmbeddingProviderManagesProcess: false,
		ModelLoaded:                     false,
	}
	p.applyStatus(http)
	p.applyStatus(http)
	p.applyStatus(http)
	if !p.snapshotNow().EmbeddingsOK {
		t.Error("EmbeddingsOK = false for an HTTP provider; want true regardless of model_loaded")
	}
}
