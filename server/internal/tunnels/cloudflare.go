package tunnels

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Cloudflare tunnel modes.
const (
	CloudflareModeNamed = "named" // pre-provisioned tunnel, stable hostname (prod)
	CloudflareModeQuick = "quick" // ephemeral *.trycloudflare.com URL (dev)
)

// restartBudget / restartWindow mirror the embeddings sidecar supervisor:
// at most 3 crash-restarts within a 5-minute window before we declare the
// provider dead.
const (
	cfRestartBudget = 3
	cfRestartWindow = 5 * time.Minute
)

// quickURLRe matches the public URL cloudflared prints to stderr when it
// brings up a quick tunnel.
var quickURLRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// CloudflareConfig is the provider's slice of configuration. Token is the
// decrypted named-tunnel connector token (empty for quick mode).
type CloudflareConfig struct {
	Mode        string // "named" | "quick"
	Token       string // named-tunnel connector token (decrypted)
	Hostname    string // named-tunnel public hostname, e.g. "cix.example.com"
	BinPath     string // cloudflared binary (name on PATH or absolute path)
	MetricsAddr string // cloudflared --metrics addr, e.g. "127.0.0.1:21848"
	StartupSec  int    // readiness probe ceiling in seconds
}

// cloudflareProvider owns the cloudflared child process. It mirrors the
// llama-server supervisor in internal/embeddings: fork+exec, exit-watcher
// goroutine, crash auto-restart with a bounded budget, graceful
// SIGTERM→SIGKILL on the process group. Readiness is the cloudflared
// metrics /ready endpoint; for quick tunnels we additionally parse stderr
// for the assigned public URL.
type cloudflareProvider struct {
	cfg       CloudflareConfig
	localPort int
	logger    *slog.Logger
	binPath   string // resolved absolute path
	token     string // resolved (file > literal)

	mu          sync.RWMutex
	cmd         *exec.Cmd
	startedAt   time.Time
	restartAt   []time.Time
	publicURL   string
	readySignal chan struct{}
	waiterDone  chan struct{}

	dead         atomic.Bool
	stopping     atomic.Bool
	lastSpawnErr atomic.Value // string
	onURLChange  atomic.Value // func(string)

	httpClient *http.Client
}

func newCloudflareProvider(cfg CloudflareConfig, localPort int, logger *slog.Logger) (*cloudflareProvider, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Mode != CloudflareModeNamed && cfg.Mode != CloudflareModeQuick {
		return nil, fmt.Errorf("cloudflare: bad mode %q (want named|quick)", cfg.Mode)
	}
	binPath, err := exec.LookPath(cfg.BinPath)
	if err != nil {
		return nil, fmt.Errorf("cloudflared binary %q not found: %w", cfg.BinPath, err)
	}
	token := strings.TrimSpace(cfg.Token)
	if cfg.Mode == CloudflareModeNamed && token == "" {
		return nil, fmt.Errorf("cloudflare named mode requires a token")
	}
	p := &cloudflareProvider{
		cfg:         cfg,
		localPort:   localPort,
		logger:      logger,
		binPath:     binPath,
		token:       token,
		readySignal: make(chan struct{}),
		waiterDone:  make(chan struct{}),
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}
	if cfg.Mode == CloudflareModeNamed {
		p.setURL(normaliseHostname(cfg.Hostname))
	}
	return p, nil
}

func (p *cloudflareProvider) Name() string { return ProviderCloudflare }

// Start spawns cloudflared and blocks until it reports ready (or the
// startup timeout fires).
func (p *cloudflareProvider) Start(ctx context.Context) error {
	return p.spawn(ctx)
}

func (p *cloudflareProvider) argv() []string {
	args := []string{"tunnel", "--no-autoupdate"}
	if p.cfg.MetricsAddr != "" {
		args = append(args, "--metrics", p.cfg.MetricsAddr)
	}
	switch p.cfg.Mode {
	case CloudflareModeQuick:
		args = append(args, "--url", fmt.Sprintf("http://localhost:%d", p.localPort))
	case CloudflareModeNamed:
		// Token is passed via the TUNNEL_TOKEN env var (see spawn), not argv,
		// so it doesn't appear in ps / /proc/<pid>/cmdline.
		args = append(args, "run")
	}
	return args
}

func (p *cloudflareProvider) spawn(ctx context.Context) error {
	argv := p.argv()
	p.logger.Info("spawning cloudflared",
		"bin", p.binPath,
		"mode", p.cfg.Mode,
		"metrics", p.cfg.MetricsAddr,
		"local_port", p.localPort,
	)

	cmd := exec.Command(p.binPath, argv...)
	// Pass the named-tunnel token via env (TUNNEL_TOKEN) rather than argv so
	// it never appears in the process table.
	if p.token != "" {
		cmd.Env = append(os.Environ(), "TUNNEL_TOKEN="+p.token)
	}
	// cloudflared writes its logs (including the quick-tunnel URL) to stderr.
	stdoutLog := newLineLogWriter(p.logger, "cloudflared.stdout", p.scanLine)
	stderrLog := newLineLogWriter(p.logger, "cloudflared.stderr", p.scanLine)
	cmd.Stdout = stdoutLog
	cmd.Stderr = stderrLog
	// Own process group so Stop() can SIGTERM the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start cloudflared: %w", err)
	}

	p.mu.Lock()
	p.cmd = cmd
	p.startedAt = time.Now()
	readyCh := make(chan struct{})
	p.readySignal = readyCh
	p.waiterDone = make(chan struct{})
	// A fresh quick tunnel gets a new URL; clear the stale one so waitReady
	// blocks until the new URL is parsed.
	if p.cfg.Mode == CloudflareModeQuick {
		p.publicURL = ""
	}
	p.mu.Unlock()

	go p.waitChild(cmd, stdoutLog, stderrLog)

	readyCtx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.StartupSec)*time.Second)
	defer cancel()
	if err := p.waitReady(readyCtx); err != nil {
		p.logger.Error("cloudflared readiness probe failed, killing child", "err", err)
		p.lastSpawnErr.Store(err.Error())
		p.killGroup()
		<-p.waiterDone
		return fmt.Errorf("cloudflared not ready: %w", err)
	}
	close(readyCh)
	p.lastSpawnErr.Store("")
	p.logger.Info("cloudflared ready", "public_url", p.URL(), "elapsed", time.Since(p.startedAt).String())
	return nil
}

// waitReady polls cloudflared's metrics /ready endpoint. For quick tunnels
// it also waits for the public URL to be parsed from stderr.
func (p *cloudflareProvider) waitReady(ctx context.Context) error {
	readyURL := "http://" + p.cfg.MetricsAddr + "/ready"
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		urlKnown := p.cfg.Mode == CloudflareModeNamed || p.URL() != ""
		if urlKnown && p.probeReady(ctx, readyURL) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// probeReady GETs the cloudflared metrics /ready endpoint, which returns 200
// only once at least one tunnel connection is registered.
func (p *cloudflareProvider) probeReady(ctx context.Context, readyURL string) bool {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, readyURL, nil)
	if err != nil {
		return false
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// scanLine is the per-log-line hook. In quick mode it watches for the
// trycloudflare URL and records it (firing the URL-change callback).
func (p *cloudflareProvider) scanLine(line string) {
	if p.cfg.Mode != CloudflareModeQuick {
		return
	}
	if m := quickURLRe.FindString(line); m != "" {
		p.setURL(m)
	}
}

// setURL records the public URL and fires the change callback when it
// actually changes.
func (p *cloudflareProvider) setURL(u string) {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" {
		return
	}
	p.mu.Lock()
	changed := p.publicURL != u
	p.publicURL = u
	p.mu.Unlock()
	if changed {
		p.logger.Info("cloudflared public URL", "url", u)
		if fn, _ := p.onURLChange.Load().(func(string)); fn != nil {
			go fn(u)
		}
	}
}

func (p *cloudflareProvider) waitChild(cmd *exec.Cmd, stdoutLog, stderrLog *lineLogWriter) {
	defer close(p.waiterDone)
	err := cmd.Wait()
	if stdoutLog != nil {
		_ = stdoutLog.Close()
	}
	if stderrLog != nil {
		_ = stderrLog.Close()
	}
	if p.stopping.Load() {
		p.logger.Info("cloudflared exited on shutdown", "err", err)
		return
	}
	p.logger.Warn("cloudflared exited unexpectedly", "err", err)
	p.restartLoop()
}

func (p *cloudflareProvider) restartLoop() {
	p.mu.Lock()
	p.restartAt = pruneTimes(p.restartAt, time.Now(), cfRestartWindow)
	p.restartAt = append(p.restartAt, time.Now())
	attempts := len(p.restartAt)
	p.mu.Unlock()

	if attempts > cfRestartBudget {
		p.logger.Error("cloudflared restart budget exhausted; tunnel dead", "attempts", attempts)
		p.dead.Store(true)
		return
	}
	backoff := time.Duration(1<<(attempts-1)) * time.Second // 1s, 2s, 4s
	p.logger.Info("restarting cloudflared after backoff", "attempt", attempts, "backoff", backoff.String())
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	<-timer.C
	if p.stopping.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.cfg.StartupSec)*time.Second+5*time.Second)
	defer cancel()
	if err := p.spawn(ctx); err != nil {
		p.logger.Error("cloudflared restart failed", "attempt", attempts, "err", err)
		p.dead.Store(true)
	}
}

// Restart tears the current child down and respawns it, clearing the
// crash-restart state so an operator's deliberate cycle doesn't count
// against the budget.
func (p *cloudflareProvider) Restart(ctx context.Context) error {
	stopCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := p.Stop(stopCtx); err != nil {
		p.logger.Warn("cloudflared: stop during restart returned error (continuing)", "err", err)
	}
	cancel()

	p.mu.Lock()
	p.restartAt = nil
	p.mu.Unlock()
	p.dead.Store(false)
	p.stopping.Store(false)

	if err := p.spawn(ctx); err != nil {
		p.dead.Store(true)
		if v, _ := p.lastSpawnErr.Load().(string); v == "" {
			p.lastSpawnErr.Store(err.Error())
		}
		return err
	}
	return nil
}

// Stop gracefully shuts cloudflared down: SIGTERM the process group, wait
// for exit (or ctx deadline), then SIGKILL.
func (p *cloudflareProvider) Stop(ctx context.Context) error {
	if !p.stopping.CompareAndSwap(false, true) {
		<-p.waiterDone
		return nil
	}
	p.mu.RLock()
	cmd := p.cmd
	waiter := p.waiterDone
	p.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		pgid = cmd.Process.Pid
	}
	p.logger.Info("sending SIGTERM to cloudflared", "pgid", pgid)
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-waiter:
		return nil
	case <-ctx.Done():
		p.logger.Warn("cloudflared SIGTERM timed out, sending SIGKILL", "pgid", pgid)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-waiter
		return ctx.Err()
	}
}

// killGroup is the emergency teardown used by spawn when the child starts
// but never becomes ready. Sets stopping so the exit-watcher does not loop
// back into restartLoop.
func (p *cloudflareProvider) killGroup() {
	p.mu.RLock()
	cmd := p.cmd
	p.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		pgid = cmd.Process.Pid
	}
	p.stopping.Store(true)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

func (p *cloudflareProvider) URL() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.publicURL
}

func (p *cloudflareProvider) OnURLChange(fn func(string)) {
	p.onURLChange.Store(fn)
}

func (p *cloudflareProvider) Status() Status {
	st := Status{Provider: ProviderCloudflare, Mode: p.cfg.Mode, Available: true}
	if v, _ := p.lastSpawnErr.Load().(string); v != "" {
		st.LastError = v
	}
	if p.dead.Load() {
		st.State = StateFailed
		return st
	}
	p.mu.RLock()
	cmd := p.cmd
	startedAt := p.startedAt
	readyCh := p.readySignal
	st.PublicURL = p.publicURL
	p.mu.RUnlock()
	if cmd != nil && cmd.Process != nil {
		st.PID = cmd.Process.Pid
	}
	st.UptimeSec = uptimeSeconds(startedAt)
	select {
	case <-readyCh:
		st.State = StateLive
	default:
		st.State = StateConnecting
	}
	return st
}

// TestConnection issues a GET to <public_url>/health. The request egresses
// to Cloudflare's edge and is routed back through the tunnel to this
// server — a true end-to-end check, not just "is the process alive".
func (p *cloudflareProvider) TestConnection(ctx context.Context) (TestResult, error) {
	return probePublicURL(ctx, p.httpClient, p.URL())
}

// pruneTimes drops timestamps older than the window. Shared crash-restart
// bookkeeping helper.
func pruneTimes(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	cutoff := now.Add(-window)
	out := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}

func normaliseHostname(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	if !strings.HasPrefix(h, "http://") && !strings.HasPrefix(h, "https://") {
		h = "https://" + h
	}
	return strings.TrimRight(h, "/")
}

// lineLogWriter forwards cloudflared output to slog line-by-line and invokes
// onLine for each complete line (used to parse the quick-tunnel URL).
type lineLogWriter struct {
	logger *slog.Logger
	source string
	onLine func(string)
	buf    []byte
}

func newLineLogWriter(logger *slog.Logger, source string, onLine func(string)) *lineLogWriter {
	return &lineLogWriter{logger: logger, source: source, onLine: onLine}
}

func (w *lineLogWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = append([]byte(nil), w.buf[i+1:]...)
		w.emit(line)
	}
	return len(p), nil
}

func (w *lineLogWriter) Close() error {
	if len(w.buf) > 0 {
		w.emit(strings.TrimRight(string(w.buf), "\r"))
		w.buf = nil
	}
	return nil
}

func (w *lineLogWriter) emit(line string) {
	if line == "" {
		return
	}
	w.logger.Info(line, "source", w.source)
	if w.onLine != nil {
		w.onLine(line)
	}
}
