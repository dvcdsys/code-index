package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dvcdsys/code-index/cli/internal/client"
)

// The launcher reads the server's state from two independent sources, because
// neither alone is sufficient:
//
//   - launchd knows whether the job is loaded and whether it has a pid. It does
//     NOT know whether the process is serving: cix-server can spend minutes
//     loading an embedding model before it answers anything.
//   - /health and /api/v1/status know whether it is serving, and what the
//     embedding provider is doing. They cannot distinguish "stopped" from
//     "starting", and cannot tell us anything at all when the job was never
//     loaded.
//
// Together they separate the three states a user actually cares about:
// stopped, starting, running.

type runState int

const (
	stateStopped runState = iota
	stateStarting
	stateRunning
)

func (s runState) String() string {
	switch s {
	case stateRunning:
		return "Running"
	case stateStarting:
		return "Starting…"
	default:
		return "Stopped"
	}
}

type snapshot struct {
	State  runState
	PID    int
	Port   int
	Status *client.StatusResponse

	// EmbeddingsOK is debounced — see poller.pollStatus.
	EmbeddingsOK bool

	// Managed is false when the launchd agent belongs to install-server.sh
	// rather than to this app; Start/Stop are then disabled.
	Managed bool
}

// providerLabel turns the wire provider kind into something a person can act
// on.
//
// The only translation is "ollama", and it is not cosmetic. In this codebase
// that kind IS the bundled llama-server supervisor — see
// server/internal/embeddings/provider/ollama/provider.go, which spawns
// llama-server and hardcodes ManagesProcess: true. No Ollama is involved and
// none can be: the HTTP providers (openai, voyage) are a different kind
// entirely. The name is historical. Printing it verbatim in a menu bar tells
// the user they are running software they have not installed, and sends them
// looking for an Ollama that is not there.
//
// The dashboard shows the raw kind, and that is fine — it is a diagnostic
// surface for someone who knows the provider model. This is not.
func providerLabel(kind string) string {
	switch kind {
	case "ollama":
		return "llama.cpp (bundled)"
	case "":
		return "unknown"
	default:
		return kind
	}
}

// EmbeddingsLine renders the provider row of the menu.
func (s snapshot) EmbeddingsLine() string {
	if s.State != stateRunning || s.Status == nil {
		return "Embeddings: unknown"
	}
	label := providerLabel(s.Status.EmbeddingProvider)
	if s.EmbeddingsOK {
		return "Embeddings: " + label + " — ready"
	}
	return "Embeddings: " + label + " — not ready"
}

// ServerLine renders the top row of the menu.
func (s snapshot) ServerLine() string {
	switch s.State {
	case stateRunning:
		return fmt.Sprintf("cix-server: Running (:%d)", s.Port)
	case stateStarting:
		return "cix-server: Starting…"
	default:
		if !s.Managed {
			return "cix-server: Stopped (managed externally)"
		}
		return "cix-server: Stopped"
	}
}

// ModelLine renders the model row, or an empty string when there is nothing to
// say — the menu hides the item rather than showing "Model: unknown".
func (s snapshot) ModelLine() string {
	if s.State != stateRunning || s.Status == nil || s.Status.EmbeddingModel == "" {
		return ""
	}
	// The server reports the provider's fingerprint ID, which is prefixed with
	// the provider kind ("ollama:awhiteside/CodeRankEmbed-Q8_0-GGUF"). The row
	// above already names the provider, and the prefix is the same misleading
	// name providerLabel exists to avoid — so show the model alone.
	model := s.Status.EmbeddingModel
	if _, rest, ok := strings.Cut(model, ":"); ok && rest != "" {
		model = rest
	}
	return "Model: " + model
}

type poller struct {
	mu   sync.RWMutex
	snap snapshot

	// notReadyStrikes counts consecutive /status polls reporting the embedding
	// provider as not ready. The server computes model_loaded under a 500 ms
	// deadline it can legitimately lose under load, so a single false is not
	// evidence of anything; flipping the menu on it produces a row that
	// flickers between ready and not-ready while nothing is wrong.
	notReadyStrikes int

	changed chan struct{}
}

const notReadyStrikesRequired = 2

func newPoller() *poller {
	return &poller{changed: make(chan struct{}, 1)}
}

func (p *poller) snapshotNow() snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snap
}

// notify wakes the menu without ever blocking the poll loop; a pending
// notification already means "re-read the snapshot".
func (p *poller) notify() {
	select {
	case p.changed <- struct{}{}:
	default:
	}
}

// run polls until stop is closed. Health is cheap and unauthenticated, so it
// runs often; /status needs the API key and returns far more than the menu
// changes, so it runs at a third of the rate.
func (p *poller) run(stop <-chan struct{}) {
	const (
		healthEvery = 5 * time.Second
		statusEvery = 15 * time.Second
	)

	p.pollHealth()
	p.pollStatus()
	p.notify()

	healthTick := time.NewTicker(healthEvery)
	statusTick := time.NewTicker(statusEvery)
	defer healthTick.Stop()
	defer statusTick.Stop()

	for {
		select {
		case <-stop:
			return
		case <-healthTick.C:
			p.pollHealth()
			p.notify()
		case <-statusTick.C:
			p.pollStatus()
			p.notify()
		}
	}
}

// refresh forces an immediate poll — used right after Start or Stop, so the
// menu reflects the click instead of waiting out the tick.
func (p *poller) refresh() {
	p.pollHealth()
	p.pollStatus()
	p.notify()
}

func (p *poller) pollHealth() {
	vars, _ := readServerEnv() // nil map on first run; serverPort falls back
	port := serverPort(vars)
	managed := !foreignAgent()

	loaded := launchdLoaded()
	pid := 0
	if loaded {
		pid = launchdPID()
	}

	healthy := client.New(localBaseURL(vars), "").Health() == nil

	state := stateStopped
	switch {
	case healthy:
		state = stateRunning
	case pid != 0:
		// A live process that is not answering yet: cold start, loading the
		// model. Reporting "Stopped" here is what makes people restart a server
		// that was two minutes from being ready.
		state = stateStarting
	}

	p.mu.Lock()
	p.snap.State = state
	p.snap.PID = pid
	p.snap.Port = port
	p.snap.Managed = managed
	if state != stateRunning {
		// Provider information from a server that is no longer answering is
		// stale by definition.
		p.snap.Status = nil
		p.snap.EmbeddingsOK = false
		p.notReadyStrikes = 0
	}
	p.mu.Unlock()
}

func (p *poller) pollStatus() {
	vars, err := readServerEnv()
	if err != nil {
		return
	}
	key := vars["CIX_API_KEY"]
	if key == "" {
		return
	}

	st, err := client.New(localBaseURL(vars), key).Status()
	if err != nil {
		return
	}
	p.applyStatus(st)
}

// applyStatus folds one /status response into the snapshot, applying the
// not-ready debounce. Split out from the fetch so the transition rules are
// testable without a server.
func (p *poller) applyStatus(st *client.StatusResponse) {
	healthy := st.EmbeddingsHealthy()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.snap.Status = st
	if healthy {
		p.notReadyStrikes = 0
		p.snap.EmbeddingsOK = true
		return
	}
	p.notReadyStrikes++
	if p.notReadyStrikes >= notReadyStrikesRequired {
		p.snap.EmbeddingsOK = false
	}
}
