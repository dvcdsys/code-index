package tunnels

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ngrok tunnel modes. We reuse the cloudflare named/quick vocabulary:
//   quick  → ephemeral random *.ngrok-free.app / *.ngrok.app URL (dev)
//   named  → reserved domain (prod), passed via --url
const (
	ngrokModeNamed = "named"
	ngrokModeQuick = "quick"
)

// ngrokStartedRe matches the JSON agent log line ngrok prints once a tunnel
// is established; the captured group is the assigned public URL. We key off
// "started tunnel" so we only latch onto the real tunnel URL (not the local
// inspection URL or other log noise).
var ngrokStartedRe = regexp.MustCompile(`"url":"(https://[^"]+)"`)

// NgrokConfig is the ngrok provider's configuration. Token is the ngrok
// authtoken (required for every tunnel, ephemeral included). Hostname is the
// reserved domain for named mode.
type NgrokConfig struct {
	Mode       string
	Token      string
	Hostname   string
	BinPath    string
	StartupSec int
}

// ngrokProvider owns the ngrok agent child process. It mirrors the
// cloudflare provider: fork+exec, exit-watcher, bounded crash-restart,
// graceful SIGTERM→SIGKILL. ngrok has no metrics /ready endpoint, so
// readiness is the "started tunnel" JSON log line (which also carries the
// public URL).
type ngrokProvider struct {
	cfg       NgrokConfig
	localPort int
	logger    *slog.Logger
	binPath   string
	token     string

	mu          sync.RWMutex
	cmd         *exec.Cmd
	startedAt   time.Time
	restartAt   []time.Time
	publicURL   string
	readySignal chan struct{}
	waiterDone  chan struct{}

	connected    atomic.Bool
	dead         atomic.Bool
	stopping     atomic.Bool
	lastSpawnErr atomic.Value // string
	onURLChange  atomic.Value // func(string)

	httpClient *http.Client
}

func newNgrokProvider(cfg NgrokConfig, localPort int, logger *slog.Logger) (*ngrokProvider, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Mode != ngrokModeNamed && cfg.Mode != ngrokModeQuick {
		return nil, fmt.Errorf("ngrok: bad mode %q (want named|quick)", cfg.Mode)
	}
	binPath, err := exec.LookPath(cfg.BinPath)
	if err != nil {
		return nil, fmt.Errorf("ngrok binary %q not found: %w", cfg.BinPath, err)
	}
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, fmt.Errorf("ngrok requires an authtoken")
	}
	if cfg.Mode == ngrokModeNamed && strings.TrimSpace(cfg.Hostname) == "" {
		return nil, fmt.Errorf("ngrok named mode requires a reserved domain")
	}
	return &ngrokProvider{
		cfg:         cfg,
		localPort:   localPort,
		logger:      logger,
		binPath:     binPath,
		token:       token,
		readySignal: make(chan struct{}),
		waiterDone:  make(chan struct{}),
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (p *ngrokProvider) Name() string { return ProviderNgrok }

func (p *ngrokProvider) Start(ctx context.Context) error { return p.spawn(ctx) }

func (p *ngrokProvider) argv() []string {
	// The authtoken is passed via NGROK_AUTHTOKEN env (see spawn), not argv,
	// so it doesn't appear in ps / /proc/<pid>/cmdline.
	args := []string{"http", strconv.Itoa(p.localPort),
		"--log", "stdout", "--log-format", "json"}
	if p.cfg.Mode == ngrokModeNamed {
		// --url takes a full origin (ngrok v3.5+); older agents used
		// --domain with a bare hostname. Bump the bundled agent if this
		// flag is rejected.
		args = append(args, "--url", normaliseHostname(p.cfg.Hostname))
	}
	return args
}

func (p *ngrokProvider) spawn(ctx context.Context) error {
	p.logger.Info("spawning ngrok", "bin", p.binPath, "mode", p.cfg.Mode, "local_port", p.localPort)

	cmd := exec.Command(p.binPath, p.argv()...)
	// Pass the authtoken via env (NGROK_AUTHTOKEN) rather than argv so it
	// never appears in the process table.
	cmd.Env = append(os.Environ(), "NGROK_AUTHTOKEN="+p.token)
	stdoutLog := newLineLogWriter(p.logger, "ngrok.stdout", p.scanLine)
	stderrLog := newLineLogWriter(p.logger, "ngrok.stderr", p.scanLine)
	cmd.Stdout = stdoutLog
	cmd.Stderr = stderrLog
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ngrok: %w", err)
	}

	p.mu.Lock()
	p.cmd = cmd
	p.startedAt = time.Now()
	readyCh := make(chan struct{})
	p.readySignal = readyCh
	p.waiterDone = make(chan struct{})
	p.publicURL = ""
	p.mu.Unlock()
	p.connected.Store(false)

	go p.waitChild(cmd, stdoutLog, stderrLog)

	readyCtx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.StartupSec)*time.Second)
	defer cancel()
	if err := p.waitReady(readyCtx); err != nil {
		p.logger.Error("ngrok readiness probe failed, killing child", "err", err)
		p.lastSpawnErr.Store(err.Error())
		p.killGroup()
		<-p.waiterDone
		return fmt.Errorf("ngrok not ready: %w", err)
	}
	close(readyCh)
	p.lastSpawnErr.Store("")
	p.logger.Info("ngrok ready", "public_url", p.URL(), "elapsed", time.Since(p.startedAt).String())
	return nil
}

// waitReady blocks until the agent reports a started tunnel (which also sets
// the public URL) or the deadline fires.
func (p *ngrokProvider) waitReady(ctx context.Context) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if p.connected.Load() && p.URL() != "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// scanLine watches the JSON agent log for the "started tunnel" line, which
// carries the assigned public URL.
func (p *ngrokProvider) scanLine(line string) {
	if !strings.Contains(line, "started tunnel") {
		return
	}
	if m := ngrokStartedRe.FindStringSubmatch(line); len(m) == 2 {
		p.setURL(m[1])
		p.connected.Store(true)
	}
}

func (p *ngrokProvider) setURL(u string) {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" {
		return
	}
	p.mu.Lock()
	changed := p.publicURL != u
	p.publicURL = u
	p.mu.Unlock()
	if changed {
		p.logger.Info("ngrok public URL", "url", u)
		if fn, _ := p.onURLChange.Load().(func(string)); fn != nil {
			go fn(u)
		}
	}
}

func (p *ngrokProvider) waitChild(cmd *exec.Cmd, stdoutLog, stderrLog *lineLogWriter) {
	defer close(p.waiterDone)
	err := cmd.Wait()
	if stdoutLog != nil {
		_ = stdoutLog.Close()
	}
	if stderrLog != nil {
		_ = stderrLog.Close()
	}
	if p.stopping.Load() {
		p.logger.Info("ngrok exited on shutdown", "err", err)
		return
	}
	p.logger.Warn("ngrok exited unexpectedly", "err", err)
	p.restartLoop()
}

func (p *ngrokProvider) restartLoop() {
	p.mu.Lock()
	p.restartAt = pruneTimes(p.restartAt, time.Now(), cfRestartWindow)
	p.restartAt = append(p.restartAt, time.Now())
	attempts := len(p.restartAt)
	p.mu.Unlock()

	if attempts > cfRestartBudget {
		p.logger.Error("ngrok restart budget exhausted; tunnel dead", "attempts", attempts)
		p.dead.Store(true)
		return
	}
	backoff := time.Duration(1<<(attempts-1)) * time.Second
	p.logger.Info("restarting ngrok after backoff", "attempt", attempts, "backoff", backoff.String())
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	<-timer.C
	if p.stopping.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.cfg.StartupSec)*time.Second+5*time.Second)
	defer cancel()
	if err := p.spawn(ctx); err != nil {
		p.logger.Error("ngrok restart failed", "attempt", attempts, "err", err)
		p.dead.Store(true)
	}
}

func (p *ngrokProvider) Restart(ctx context.Context) error {
	stopCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := p.Stop(stopCtx); err != nil {
		p.logger.Warn("ngrok: stop during restart returned error (continuing)", "err", err)
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

func (p *ngrokProvider) Stop(ctx context.Context) error {
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
	p.logger.Info("sending SIGTERM to ngrok", "pgid", pgid)
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-waiter:
		return nil
	case <-ctx.Done():
		p.logger.Warn("ngrok SIGTERM timed out, sending SIGKILL", "pgid", pgid)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-waiter
		return ctx.Err()
	}
}

func (p *ngrokProvider) killGroup() {
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

func (p *ngrokProvider) URL() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.publicURL
}

func (p *ngrokProvider) OnURLChange(fn func(string)) { p.onURLChange.Store(fn) }

func (p *ngrokProvider) Status() Status {
	st := Status{Provider: ProviderNgrok, Mode: p.cfg.Mode, Available: true}
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

func (p *ngrokProvider) TestConnection(ctx context.Context) (TestResult, error) {
	return probePublicURL(ctx, p.httpClient, p.URL())
}
