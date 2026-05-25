package tunnels

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// ManagerConfig is deployment infra only — where each provider's binary
// lives and its tuning knobs. The feature has no enable flag here: whether a
// tunnel runs and how it's configured is supplied at runtime via Apply
// (sourced from the dashboard-managed DB row). main.go populates this so
// the package does not import the config package.
type ManagerConfig struct {
	LocalPort  int // CIX_PORT — the local target tunnels point at
	Cloudflare CloudflareInfra
	Ngrok      NgrokInfra

	// BinManaged enables downloading/updating provider binaries from the
	// dashboard into BinDir (set by the Docker images). When off, binaries
	// come from the configured path / system PATH only.
	BinManaged bool
	BinDir     string
}

// CloudflareInfra is the deployment-side config for the cloudflare provider.
type CloudflareInfra struct {
	BinPath     string // cloudflared binary path
	MetricsAddr string // cloudflared --metrics addr
	StartupSec  int    // readiness probe ceiling
}

// NgrokInfra is the deployment-side config for the ngrok provider.
type NgrokInfra struct {
	BinPath    string // ngrok binary path
	StartupSec int    // readiness probe ceiling
}

// Settings is the runtime, dashboard-managed configuration applied to the
// manager. Token carries the decrypted Cloudflare connector token (named
// mode only).
type Settings struct {
	Enabled  bool
	Provider string
	Mode     string
	Hostname string
	Token    string
}

// Manager owns the active tunnel provider (if any). It is the seam the rest
// of the server talks to: URL() feeds webhook URL construction,
// Status()/Catalog() feed the dashboard, OnURLChange wires the reconciler,
// and Apply (re)configures the running tunnel at runtime.
type Manager struct {
	logger *slog.Logger
	infra  ManagerConfig

	installer *Installer // nil unless BinManaged

	// applyMu serializes the whole Apply operation so two concurrent applies
	// can't interleave their stop-old / start-new steps and leak an orphan
	// provider. mu (below) only guards individual field reads/writes.
	applyMu sync.Mutex

	mu           sync.RWMutex
	active       Provider
	onURLChange  func(string)
	lastSettings Settings
	applyErr     string
}

// NewManager constructs a manager with infra defaults. It does NOT start a
// tunnel — call Apply with the dashboard-managed settings (main.go does this
// on boot from the persisted config).
func NewManager(infra ManagerConfig, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{logger: logger, infra: infra}
	if infra.BinManaged && infra.BinDir != "" {
		m.installer = NewInstaller(infra.BinDir, logger)
	}
	return m
}

// configuredBinPath returns the env-configured binary path for a provider.
func (m *Manager) configuredBinPath(provider string) string {
	if provider == ProviderNgrok {
		return m.infra.Ngrok.BinPath
	}
	return m.infra.Cloudflare.BinPath
}

// effectiveBinPath prefers a managed (downloaded) binary when present, else
// falls back to the configured path / PATH name.
func (m *Manager) effectiveBinPath(provider string) string {
	if m.installer != nil {
		if ok, p := m.installer.Installed(provider); ok {
			return p
		}
	}
	return m.configuredBinPath(provider)
}

// Binaries reports the agent-binary status for every provider so the
// dashboard can show install instructions (local) or Install/Update
// buttons (managed).
func (m *Manager) Binaries(ctx context.Context) []BinaryStatus {
	managed := m.installer != nil
	out := make([]BinaryStatus, 0, 2)
	for _, prov := range []string{ProviderCloudflare, ProviderNgrok} {
		st := BinaryStatus{Provider: prov, Managed: managed}
		if managed {
			if ok, p := m.installer.Installed(prov); ok {
				st.Installed, st.Path = true, p
			}
		}
		if !st.Installed {
			if p, err := exec.LookPath(m.configuredBinPath(prov)); err == nil {
				st.Installed, st.Path = true, p
			}
		}
		if st.Installed {
			st.Version = binaryVersion(ctx, prov, st.Path)
		}
		out = append(out, st)
	}
	return out
}

// InstallBinary downloads (or updates) a provider's agent binary into the
// managed directory. Only available when BinManaged is on.
func (m *Manager) InstallBinary(ctx context.Context, provider string) error {
	if m.installer == nil {
		return ErrBinManagementDisabled
	}
	if provider != ProviderCloudflare && provider != ProviderNgrok {
		return fmt.Errorf("unknown provider %q", provider)
	}
	return m.installer.Install(ctx, provider)
}

// Apply reconfigures the tunnel to match settings: it tears down any current
// provider and, when enabled, builds and starts a new one. A start failure
// is recorded (surfaced via Status) but the provider is kept so the operator
// can see the error and retry — Apply returns the error for the caller to
// relay, it does not panic the server.
func (m *Manager) Apply(ctx context.Context, set Settings) error {
	// Serialize the entire apply so concurrent calls can't interleave the
	// stop-old / start-new steps (which would orphan a running provider).
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	m.mu.Lock()
	m.lastSettings = set
	old := m.active
	m.active = nil
	cb := m.onURLChange
	m.mu.Unlock()

	if old != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = old.Stop(stopCtx)
		cancel()
	}

	if !set.Enabled {
		m.setApplyErr("")
		return nil
	}
	provider := set.Provider
	if provider == "" {
		provider = ProviderCloudflare
	}

	var p Provider
	var err error
	switch provider {
	case ProviderCloudflare:
		p, err = newCloudflareProvider(CloudflareConfig{
			Mode:        set.Mode,
			Token:       set.Token,
			Hostname:    set.Hostname,
			BinPath:     m.effectiveBinPath(ProviderCloudflare),
			MetricsAddr: m.infra.Cloudflare.MetricsAddr,
			StartupSec:  m.infra.Cloudflare.StartupSec,
		}, m.infra.LocalPort, m.logger)
	case ProviderNgrok:
		p, err = newNgrokProvider(NgrokConfig{
			Mode:       set.Mode,
			Token:      set.Token,
			Hostname:   set.Hostname,
			BinPath:    m.effectiveBinPath(ProviderNgrok),
			StartupSec: m.infra.Ngrok.StartupSec,
		}, m.infra.LocalPort, m.logger)
	default:
		err = fmt.Errorf("tunnel provider %q is not recognised", provider)
	}
	if err != nil {
		m.setApplyErr(err.Error())
		return err
	}
	if cb != nil {
		p.OnURLChange(cb)
	}
	m.mu.Lock()
	m.active = p
	m.applyErr = ""
	m.mu.Unlock()

	if serr := p.Start(ctx); serr != nil {
		m.setApplyErr(serr.Error())
		return serr
	}
	return nil
}

func (m *Manager) setApplyErr(s string) {
	m.mu.Lock()
	m.applyErr = s
	m.mu.Unlock()
}

// URL returns the active tunnel's public base URL, or "" when there is no
// live tunnel. buildWebhookURL prefers this over CIX_PUBLIC_URL.
func (m *Manager) URL() string {
	m.mu.RLock()
	p := m.active
	m.mu.RUnlock()
	if p == nil {
		return ""
	}
	return p.URL()
}

// Status returns the active provider snapshot, or a disabled/failed
// placeholder when no provider is running.
func (m *Manager) Status() Status {
	m.mu.RLock()
	p := m.active
	applyErr := m.applyErr
	enabled := m.lastSettings.Enabled
	m.mu.RUnlock()
	if p != nil {
		return p.Status()
	}
	provider := m.lastSettings.Provider
	if provider == "" {
		provider = ProviderCloudflare
	}
	st := Status{Provider: provider, State: StateDisabled, Available: true}
	if enabled && applyErr != "" {
		st.State = StateFailed
		st.LastError = applyErr
	}
	return st
}

// Catalog lists every known provider. The active provider carries live
// status; the others are reported as available-but-disabled (selectable
// from the dashboard).
func (m *Manager) Catalog() []Status {
	active := m.Status()
	out := make([]Status, 0, 2)
	for _, name := range []string{ProviderCloudflare, ProviderNgrok} {
		if active.Provider == name {
			out = append(out, active)
		} else {
			out = append(out, Status{Provider: name, State: StateDisabled, Available: true})
		}
	}
	return out
}

// TestConnection runs the active provider's end-to-end probe.
func (m *Manager) TestConnection(ctx context.Context) (TestResult, error) {
	m.mu.RLock()
	p := m.active
	m.mu.RUnlock()
	if p == nil {
		return TestResult{}, ErrNoActiveTunnel
	}
	return p.TestConnection(ctx)
}

// Restart re-applies the last settings, recreating the tunnel subprocess.
func (m *Manager) Restart(ctx context.Context) error {
	m.mu.RLock()
	set := m.lastSettings
	m.mu.RUnlock()
	if !set.Enabled {
		return ErrNoActiveTunnel
	}
	return m.Apply(ctx, set)
}

// Stop tears the active provider down within the ctx deadline.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.RLock()
	p := m.active
	m.mu.RUnlock()
	if p == nil {
		return nil
	}
	return p.Stop(ctx)
}

// OnURLChange registers the callback fired when the active tunnel's public
// URL becomes known or changes. It persists across Apply (re-attached to
// each new provider).
func (m *Manager) OnURLChange(fn func(string)) {
	m.mu.Lock()
	m.onURLChange = fn
	p := m.active
	m.mu.Unlock()
	if p != nil {
		p.OnURLChange(fn)
	}
}
