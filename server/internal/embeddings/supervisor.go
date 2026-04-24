package embeddings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// darwinSunPathMax is the platform limit for unix socket paths on macOS.
// Including the terminating NUL byte the kernel accepts 104 characters.
const darwinSunPathMax = 104

// restartBudget bounds how many consecutive crash-restart cycles we allow
// before declaring the supervisor dead. Matches the plan (max 3 retries).
const restartBudget = 3

// restartWindow bounds the time window in which restartBudget applies.
// Outside the window we reset the counter — a llama-server that has been
// happy for 10 minutes then crashes once should not immediately be fatal.
const restartWindow = 5 * time.Minute

// supervisorConfig bundles everything the supervisor needs to fork+exec and
// talk to llama-server. It is populated by Service.New from *config.Config
// so the supervisor does not import the config package directly.
type supervisorConfig struct {
	BinDir       string // where llama-server + dylibs live
	GGUFPath     string // absolute path to the model file
	SocketPath   string // unix socket path (only used when Transport == "unix")
	Transport    string // "unix" or "tcp"
	CtxSize      int
	NGpuLayers   int
	StartupSec   int
	TCPPort      int // 0 = auto-pick, only relevant for tcp transport
}

// supervisor owns the llama-server child process. It is responsible for:
//   - fork+exec with the correct argv + env
//   - probing /health until ready
//   - auto-restart on unexpected exit (up to restartBudget within restartWindow)
//   - graceful SIGTERM on Stop (respecting the caller's context deadline)
//
// Only one instance should exist per cix-server process. Concurrent access is
// safe — all state reads go through the RWMutex.
type supervisor struct {
	cfg    supervisorConfig
	logger *slog.Logger

	// client is read by Service.Embed*; it becomes non-nil once we decide on
	// a transport (unix or tcp fallback). It is not swapped during the
	// supervisor's lifetime — only the underlying child process is restarted.
	client *llamaClient

	mu          sync.RWMutex
	cmd         *exec.Cmd
	startedAt   time.Time
	restartAt   []time.Time // timestamps of recent restarts; pruned to window
	dead        atomic.Bool // true when we exhausted the restart budget
	stopping    atomic.Bool // true when Stop has been invoked
	readySignal chan struct{}

	waiterDone chan struct{} // closed after the exit-watcher goroutine returns
}

// newSupervisor validates the config, clamps the transport if needed, and
// spawns the initial child. It blocks until the child is ready (or the
// startup timeout fires), so callers can rely on the service being live the
// moment this function returns.
func newSupervisor(ctx context.Context, cfg supervisorConfig, logger *slog.Logger) (*supervisor, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := validateSupervisorConfig(&cfg, logger); err != nil {
		return nil, err
	}

	// Decide transport + build client once. The child process can die and
	// restart, but the socket path / tcp port stay the same, so the client
	// does not need to be recreated.
	s := &supervisor{
		cfg:         cfg,
		logger:      logger,
		readySignal: make(chan struct{}),
		waiterDone:  make(chan struct{}),
	}
	switch cfg.Transport {
	case "unix":
		s.client = newUnixClient(cfg.SocketPath)
	case "tcp":
		s.client = newTCPClient("127.0.0.1", cfg.TCPPort)
	default:
		return nil, fmt.Errorf("supervisor: bad transport %q", cfg.Transport)
	}

	if err := s.spawn(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// validateSupervisorConfig clamps unsafe settings. The most common issue is
// the macOS socket-path length; on violation we fall back to TCP rather than
// letting the child fail with an opaque bind error.
func validateSupervisorConfig(cfg *supervisorConfig, logger *slog.Logger) error {
	binPath := filepath.Join(cfg.BinDir, "llama-server")
	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("llama-server not found at %s (run `make fetch-llama` or `make bundle`): %w", binPath, err)
	}
	if cfg.GGUFPath == "" {
		return errors.New("supervisor: GGUFPath is required")
	}
	if _, err := os.Stat(cfg.GGUFPath); err != nil {
		return fmt.Errorf("gguf not found at %s: %w", cfg.GGUFPath, err)
	}

	if cfg.Transport == "unix" && runtime.GOOS == "darwin" && len(cfg.SocketPath) > darwinSunPathMax {
		logger.Warn("unix socket path exceeds darwin sun_path limit; falling back to TCP",
			"socket_path_len", len(cfg.SocketPath),
			"limit", darwinSunPathMax,
		)
		cfg.Transport = "tcp"
	}
	if cfg.Transport == "tcp" && cfg.TCPPort == 0 {
		port, err := pickFreePort()
		if err != nil {
			return fmt.Errorf("pick free port: %w", err)
		}
		cfg.TCPPort = port
	}
	return nil
}

// pickFreePort asks the kernel to allocate a port, closes the listener, and
// returns the number. Classic TOCTOU race but acceptable for single-process
// supervisor startup.
func pickFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// spawn fork+execs llama-server, starts the exit-watcher goroutine, and
// blocks until the readiness probe succeeds or the startup timeout expires.
// On readiness-probe failure it tears the child process down so the caller
// does not leak a zombie.
func (s *supervisor) spawn(ctx context.Context) error {
	binPath := filepath.Join(s.cfg.BinDir, "llama-server")

	argv := []string{
		"-m", s.cfg.GGUFPath,
		"--embeddings",
		// "cls" matches the pooling that the Python llama-cpp-python wheel
		// uses for CodeRankEmbed-Q8_0. Empirically verified 2026-04-24:
		//   "last" -> mean cosine 0.66 (uniform drift vs reference)
		//   "mean" -> mean cosine 0.89 (better but not parity)
		//   "cls"  -> mean cosine 1.000, min 0.999999 (gate passes)
		// If a future model needs a different pooling, plumb it through config
		// rather than hardcoding per-model rules here.
		"--pooling", "cls",
		"--ctx-size", strconv.Itoa(s.cfg.CtxSize),
		// n_ubatch must be >= ctx-size so single chunks up to CtxSize tokens
		// can be embedded in one pass. Without this flag llama-server defaults
		// n_ubatch=512 and auto-resets n_batch to match, causing HTTP 500 for
		// any chunk larger than 512 tokens.
		"--ubatch-size", strconv.Itoa(s.cfg.CtxSize),
		"--n-gpu-layers", strconv.Itoa(s.cfg.NGpuLayers),
	}
	switch s.cfg.Transport {
	case "unix":
		// Clear any stale socket file from a previous crashed run.
		if err := os.Remove(s.cfg.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale socket: %w", err)
		}
		argv = append(argv,
			"--host", s.cfg.SocketPath,
			// --port is ignored when --host is a socket path, but llama-server
			// refuses to start without it, so we pass a placeholder.
			"--port", "8080",
		)
	case "tcp":
		argv = append(argv,
			"--host", "127.0.0.1",
			"--port", strconv.Itoa(s.cfg.TCPPort),
		)
	}

	s.logger.Info("spawning llama-server",
		"bin", binPath,
		"transport", s.cfg.Transport,
		"socket", s.cfg.SocketPath,
		"port", s.cfg.TCPPort,
		"gguf", s.cfg.GGUFPath,
		"ctx", s.cfg.CtxSize,
		"n_gpu_layers", s.cfg.NGpuLayers,
	)

	cmd := exec.Command(binPath, argv...)
	// Keep references to the log writers so waitChild can flush any trailing
	// partial line after the child exits (n1 — otherwise the very last line
	// of a crash log is silently dropped when it lacks a newline).
	stdoutLog := newLogWriter(s.logger, slog.LevelInfo, "llama-server.stdout")
	stderrLog := newLogWriter(s.logger, slog.LevelInfo, "llama-server.stderr")
	cmd.Stdout = stdoutLog
	cmd.Stderr = stderrLog
	// Defense-in-depth for dylib resolution on darwin. Official llama.cpp
	// macOS builds already use @loader_path rpath so this is belt-and-braces.
	cmd.Env = append(os.Environ(), "DYLD_LIBRARY_PATH="+s.cfg.BinDir)
	// Put the child in its own process group so Stop() can SIGTERM the whole
	// group — llama-server may fork helper threads/processes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start llama-server: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.startedAt = time.Now()
	// Recreate the ready channel in case this is a restart — each spawn needs
	// its own signal so Embed callers can block for the newly-spawning child.
	s.readySignal = make(chan struct{})
	s.waiterDone = make(chan struct{})
	s.mu.Unlock()

	// Exit-watcher: if the child dies unexpectedly, try to restart. The log
	// writers are closed inside waitChild to flush any trailing partial line.
	go s.waitChild(cmd, stdoutLog, stderrLog)

	// Readiness probe: poll /health until success or timeout.
	readyCtx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.StartupSec)*time.Second)
	defer cancel()
	if err := s.waitReady(readyCtx); err != nil {
		s.logger.Error("llama-server readiness probe failed, killing child", "err", err)
		s.killGroup()
		<-s.waiterDone
		return fmt.Errorf("%w: %v", ErrNotReady, err)
	}
	close(s.readySignal)
	s.logger.Info("llama-server ready", "elapsed", time.Since(s.startedAt).String())
	return nil
}

// waitReady polls the /health endpoint every 200ms until it returns 200 or
// the context deadline fires. For unix transport we also require the socket
// file to appear on disk before we issue the first HTTP call.
func (s *supervisor) waitReady(ctx context.Context) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		// unix socket must exist before the first connect attempt.
		if s.cfg.Transport == "unix" {
			if _, err := os.Stat(s.cfg.SocketPath); err == nil {
				probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				err := s.client.Health(probeCtx)
				cancel()
				if err == nil {
					return nil
				}
			}
		} else {
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := s.client.Health(probeCtx)
			cancel()
			if err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// waitChild reaps the child process. If the exit was due to a Stop() call we
// just record the result; otherwise we trigger the restart loop. Log writers
// are closed here so any unterminated final line is flushed before we move on
// — particularly important for crash-exit logs (n1 fix).
func (s *supervisor) waitChild(cmd *exec.Cmd, stdoutLog, stderrLog *logWriter) {
	defer close(s.waiterDone)

	err := cmd.Wait()

	// Flush trailing partial lines before going quiet. Errors here are not
	// actionable, so we discard them.
	if stdoutLog != nil {
		_ = stdoutLog.Close()
	}
	if stderrLog != nil {
		_ = stderrLog.Close()
	}

	if s.stopping.Load() {
		s.logger.Info("llama-server exited on shutdown", "err", err)
		return
	}

	s.logger.Warn("llama-server exited unexpectedly", "err", err)
	s.restartLoop()
}

// restartLoop implements the exponential-backoff restart policy. It runs on
// the exit-watcher goroutine so only one restart is in flight at a time.
func (s *supervisor) restartLoop() {
	s.mu.Lock()
	s.restartAt = pruneRestarts(s.restartAt, time.Now(), restartWindow)
	s.restartAt = append(s.restartAt, time.Now())
	attempts := len(s.restartAt)
	s.mu.Unlock()

	if attempts > restartBudget {
		s.logger.Error("restart budget exhausted; supervisor dead", "attempts", attempts)
		s.dead.Store(true)
		return
	}

	backoff := time.Duration(1<<(attempts-1)) * time.Second // 1s, 2s, 4s
	s.logger.Info("restarting llama-server after backoff", "attempt", attempts, "backoff", backoff.String())

	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-timer.C:
	}
	if s.stopping.Load() {
		return
	}
	// Fresh context — we do not have the original Service context here, and
	// startup must bound its own wait regardless.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg.StartupSec)*time.Second+5*time.Second)
	defer cancel()
	if err := s.spawn(ctx); err != nil {
		s.logger.Error("restart failed", "attempt", attempts, "err", err)
		// Force next exit-watcher tick to count this as another failure.
		s.dead.Store(true)
		return
	}
}

// pruneRestarts drops restart timestamps older than the window. Keeps the
// slice bounded and makes the "N restarts in window" check correct across
// long-running processes.
func pruneRestarts(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	cutoff := now.Add(-window)
	out := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}

// Stop gracefully shuts down llama-server. It first sends SIGTERM to the
// child's process group, waits for exit (or ctx deadline), then SIGKILLs if
// the graceful path failed. The caller's context controls the deadline —
// main.go already uses a 10s shutdown context.
func (s *supervisor) Stop(ctx context.Context) error {
	if !s.stopping.CompareAndSwap(false, true) {
		// Already stopping; just wait for the existing teardown.
		<-s.waiterDone
		return nil
	}

	s.mu.RLock()
	cmd := s.cmd
	s.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		pgid = cmd.Process.Pid // fall back to single-pid signal
	}
	s.logger.Info("sending SIGTERM to llama-server", "pgid", pgid)
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	select {
	case <-s.waiterDone:
		// Also clean up the socket file so a subsequent run does not trip on it.
		if s.cfg.Transport == "unix" {
			_ = os.Remove(s.cfg.SocketPath)
		}
		return nil
	case <-ctx.Done():
		s.logger.Warn("SIGTERM timed out, sending SIGKILL", "pgid", pgid)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-s.waiterDone
		if s.cfg.Transport == "unix" {
			_ = os.Remove(s.cfg.SocketPath)
		}
		return ctx.Err()
	}
}

// killGroup is the emergency teardown path used by spawn() when the child
// starts but never becomes ready. It differs from Stop in that it does not
// flip the `stopping` flag — the caller is responsible for sequencing.
//
// Circuit-breaker semantics (A2): setting `stopping = true` is intentional
// here. It tells the waitChild goroutine that this exit is a deliberate kill,
// not a crash, so it does NOT loop back into restartLoop. The caller
// (spawn → waitReady fail → killGroup) then sees the waiter finish, returns
// ErrNotReady to restartLoop, which sets `dead = true`. Net effect: a failed
// readiness probe after repeated restarts permanently marks the supervisor
// dead instead of looping forever trying to restart a broken child.
//
// There is no supported path to reset `stopping` after this — bring the
// service back by recreating the whole embeddings.Service on the next
// process restart (in practice: container restart or cix-server reboot).
func (s *supervisor) killGroup() {
	s.mu.RLock()
	cmd := s.cmd
	s.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		pgid = cmd.Process.Pid
	}
	s.stopping.Store(true)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	if s.cfg.Transport == "unix" {
		_ = os.Remove(s.cfg.SocketPath)
	}
}

// Ready blocks until the current child is ready or ctx expires.
func (s *supervisor) Ready(ctx context.Context) error {
	if s.dead.Load() {
		return ErrSupervisor
	}
	s.mu.RLock()
	ch := s.readySignal
	s.mu.RUnlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
