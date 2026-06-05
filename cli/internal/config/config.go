package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Servers is the canonical list of named cix servers the CLI can talk to.
	// One of them is the default (DefaultServer). Commands target the default
	// unless --server <name> is given.
	Servers []ServerEntry `yaml:"servers" key:"servers" desc:"Named cix servers; the entry matching default_server is the active one"`
	// DefaultServer is the name of the server used when --server is absent.
	// CIX_SERVER env var overrides it when --server is not given.
	DefaultServer string `yaml:"default_server" key:"default_server" env:"CIX_SERVER" desc:"Alias of the server used when --server is omitted"`

	// API is the legacy single-server config (pre-multi-server). It is read
	// from old config files and migrated into Servers on Load (see
	// migrateToServers), then cleared so it is no longer written back.
	// omitempty keeps it out of freshly-written configs.
	API APIConfig `yaml:"api,omitempty"`

	Watcher  WatcherConfig  `yaml:"watcher"`
	Indexing IndexingConfig `yaml:"indexing"`
	Projects []ProjectEntry `yaml:"projects" key:"projects" desc:"Registered project paths and their auto-watch flag"`
}

// ServerEntry is a single named cix server: a friendly alias plus its base
// URL and API key. The alias is what users pass to --server.
//
// CIX_API_URL / CIX_API_KEY do NOT bind to a specific entry — they override
// the URL/key of the *resolved* server (the one picked by --server or
// default_server) inside getClient. Hence no `env:` tags here.
type ServerEntry struct {
	Name string `yaml:"name" desc:"Server alias"`
	URL  string `yaml:"url" desc:"Base URL of the cix server" validate:"omitempty,url"`
	Key  string `yaml:"key" desc:"API key (bearer token)" sensitive:"true"`
	// Headers are extra HTTP headers attached to every request the CLI makes
	// to this server (in addition to the cix Bearer). They let the client
	// satisfy an authenticating reverse proxy / Zero-Trust gateway in front
	// of cix — e.g. a Cloudflare Access service token
	// (CF-Access-Client-Id / CF-Access-Client-Secret). Values support
	// ${ENV} expansion at request time so secrets stay out of the file.
	// Opt-in: absent = current behavior.
	Headers map[string]string `yaml:"headers,omitempty" sensitive:"true" desc:"Extra HTTP headers sent on every request (values support ${ENV} expansion)"`
}

type APIConfig struct {
	URL string `yaml:"url"`
	Key string `yaml:"key"`
}

// DefaultServerName is the alias assigned to the implicit/migrated server.
const DefaultServerName = "default"

type WatcherConfig struct {
	Enabled          bool     `yaml:"enabled" key:"watcher.enabled" desc:"Run the file watcher" default:"true"`
	DebounceMS       int      `yaml:"debounce_ms" key:"watcher.debounce_ms" desc:"Debounce delay (ms)" default:"5000" validate:"min=100,max=60000"`
	ExcludePatterns  []string `yaml:"exclude" key:"watcher.exclude" desc:"Paths/globs to skip (comma-separated; REPLACE semantics on set)" default:"node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store"`
	SyncIntervalMins int      `yaml:"sync_interval_mins" key:"watcher.sync_interval_mins" desc:"Periodic sync interval (minutes)" default:"5" validate:"min=1"`
}

// ServerConfig used to hold port/cache_ttl knobs for an in-process server.
// The CLI runs no server, so the struct and its fields were removed. Old
// `server:` blocks in existing config files still load without error —
// koanf silently drops unknown keys during unmarshal.

type IndexingConfig struct {
	BatchSize int `yaml:"batch_size" key:"indexing.batch_size" desc:"Indexing batch size" default:"20" validate:"min=1"`

	// StreamingIdleTimeoutSec is the maximum allowed silence on the streaming
	// /index/files response before the CLI gives up and closes the conn. The
	// server emits a heartbeat every 10s, so 30s gives the network three
	// retry windows. Set to 0 to disable the watchdog (not recommended).
	StreamingIdleTimeoutSec int `yaml:"streaming_idle_timeout_sec" key:"indexing.streaming_idle_timeout_sec" desc:"Streaming /index/files idle timeout (seconds); 0 disables watchdog" default:"30" validate:"min=0"`
}

type ProjectEntry struct {
	Path      string `yaml:"path" desc:"Absolute path of the project root"`
	AutoWatch bool   `yaml:"auto_watch" desc:"Start the file watcher automatically for this project"`
}

var (
	globalConfig *Config
	configPath   string
)

// (defaults() removed: the canonical source of default values is now the
// `default:"…"` struct tag set; loadWithKoanf seeds them via the schema
// walker. Servers/DefaultServer are populated by migrateToServers as before.)

// normalizeLegacyKeys maps old viper-generated YAML key names to the current
// yaml struct tag names. Provides backward compatibility for configs created
// before the viper→yaml.v3 migration.
func normalizeLegacyKeys(data []byte) []byte {
	for _, pair := range [][2]string{
		{"debouncems:", "debounce_ms:"},
		{"excludepatterns:", "exclude:"},
		{"cachettl:", "cache_ttl:"},
		{"autowatch:", "auto_watch:"},
		// `batchsize:` was the viper-mangled emission of the `BatchSize` Go
		// field. We now use `batch_size:` everywhere (consistent with other
		// snake_case keys); this mapping keeps old files loading.
		{"batchsize:", "batch_size:"},
	} {
		data = bytes.ReplaceAll(data, []byte(pair[0]), []byte(pair[1]))
	}
	return data
}

// Load loads configuration from ~/.cix/config.yaml.
// Fields absent from the file keep their default values.
//
// Implementation: delegates the heavy lifting to loadWithKoanf
// (loader_koanf.go) — defaults come from struct tags, the YAML file is the
// override layer, and migrateToServers handles the legacy `api:` block and
// the implicit localhost seeding. Load owns the singleton cache and the
// "re-save when the loader rewrote the on-disk form" side effect.
func Load() (*Config, error) {
	if globalConfig != nil {
		return globalConfig, nil
	}

	_, path, err := configPaths()
	if err != nil {
		return nil, err
	}
	configPath = path

	cfg, needsResave, err := loadWithKoanf()
	if err != nil {
		return nil, err
	}

	globalConfig = cfg
	if needsResave {
		_ = Save(cfg)
	}
	return globalConfig, nil
}

// migrateToServers upgrades a parsed config to the multi-server layout in
// place and reports whether anything changed (so Load can re-save).
//
//   - Legacy single-server config (api: only, no servers:) → one server named
//     "default" carrying the old url/key; api: is cleared so it is no longer
//     written back.
//   - servers: present but no default_server → default to the first entry.
//   - Neither servers: nor api: (e.g. a partial hand-written file) → seed the
//     implicit localhost default so the CLI is always usable.
func migrateToServers(cfg *Config) (changed bool) {
	if len(cfg.Servers) == 0 {
		url := cfg.API.URL
		if url == "" {
			// No api.url (fresh install, or a legacy file that only set
			// api.key): fall back to the historical localhost default so the
			// migrated server is usable.
			url = "http://localhost:21847"
		}
		cfg.Servers = []ServerEntry{
			{Name: DefaultServerName, URL: url, Key: cfg.API.Key},
		}
		cfg.DefaultServer = DefaultServerName
		cfg.API = APIConfig{}
		return true
	}

	// Servers present. Clear any leftover legacy api block and ensure a default.
	if cfg.API.URL != "" || cfg.API.Key != "" {
		cfg.API = APIConfig{}
		changed = true
	}
	if cfg.DefaultServer == "" {
		cfg.DefaultServer = cfg.Servers[0].Name
		changed = true
	}
	return changed
}

// Save writes cfg to disk and updates the in-memory singleton.
func Save(cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	globalConfig = cfg
	return nil
}

// GetConfigPath returns the path to the config file.
func GetConfigPath() string {
	return configPath
}

// ResetForTesting clears the in-memory singleton.
// Only intended for use in tests.
func ResetForTesting() {
	globalConfig = nil
	configPath = ""
}

// validateServerName checks a server alias is usable as both a YAML name and
// a `server.<name>.url` config key (which is split on "."). Names must be
// non-empty and contain no dots or whitespace.
func validateServerName(name string) error {
	if name == "" {
		return fmt.Errorf("server name must not be empty")
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return fmt.Errorf("server name %q must not contain whitespace", name)
	}
	if strings.Contains(name, ".") {
		return fmt.Errorf("server name %q must not contain '.'", name)
	}
	return nil
}

// GetServer returns a pointer to the server entry with the given name.
func (c *Config) GetServer(name string) (*ServerEntry, bool) {
	for i := range c.Servers {
		if c.Servers[i].Name == name {
			return &c.Servers[i], true
		}
	}
	return nil, false
}

// DefaultServerEntry returns the configured default server, falling back to
// the first server when DefaultServer is unset or dangling.
func (c *Config) DefaultServerEntry() (*ServerEntry, bool) {
	if c.DefaultServer != "" {
		if s, ok := c.GetServer(c.DefaultServer); ok {
			return s, true
		}
	}
	if len(c.Servers) > 0 {
		return &c.Servers[0], true
	}
	return nil, false
}

// ResolveServer selects which server a command should target. An empty name
// means "use the default"; a non-empty name must match a configured alias
// exactly. On miss the error lists the available aliases.
func (c *Config) ResolveServer(name string) (*ServerEntry, error) {
	if name == "" {
		if s, ok := c.DefaultServerEntry(); ok {
			return s, nil
		}
		return nil, fmt.Errorf("no servers configured; run 'cix config set api.url <url>' or 'cix config set server.<name>.url <url>'")
	}
	if s, ok := c.GetServer(name); ok {
		return s, nil
	}
	return nil, fmt.Errorf("server %q not found; configured servers:\n  - %s", name, strings.Join(c.serverNames(), "\n  - "))
}

// serverNames returns the aliases of all configured servers.
func (c *Config) serverNames() []string {
	names := make([]string, 0, len(c.Servers))
	for _, s := range c.Servers {
		names = append(names, s.Name)
	}
	return names
}

// upsertServer finds or appends the named server, applies mut, and makes it
// the default when no default is set yet.
func upsertServer(cfg *Config, name string, mut func(*ServerEntry)) {
	if s, ok := cfg.GetServer(name); ok {
		mut(s)
	} else {
		cfg.Servers = append(cfg.Servers, ServerEntry{Name: name})
		mut(&cfg.Servers[len(cfg.Servers)-1])
	}
	if cfg.DefaultServer == "" {
		cfg.DefaultServer = name
	}
}

// SetServerURL sets (or creates) the URL of the named server and persists.
func SetServerURL(name, url string) error {
	if err := validateServerName(name); err != nil {
		return err
	}
	cfg, err := Load()
	if err != nil {
		return err
	}
	upsertServer(cfg, name, func(s *ServerEntry) { s.URL = url })
	return Save(cfg)
}

// SetServerKey sets (or creates) the API key of the named server and persists.
func SetServerKey(name, key string) error {
	if err := validateServerName(name); err != nil {
		return err
	}
	cfg, err := Load()
	if err != nil {
		return err
	}
	upsertServer(cfg, name, func(s *ServerEntry) { s.Key = key })
	return Save(cfg)
}

// SetServerHeader sets (or creates) a custom HTTP header on the named server
// and persists. The header name must be a valid HTTP token and the value must
// not contain CR/LF (header-injection guard). Creates the server entry if it
// does not exist yet, mirroring SetServerURL/SetServerKey.
func SetServerHeader(name, headerName, value string) error {
	if err := validateServerName(name); err != nil {
		return err
	}
	if err := validateHeader(headerName, value); err != nil {
		return err
	}
	cfg, err := Load()
	if err != nil {
		return err
	}
	upsertServer(cfg, name, func(s *ServerEntry) {
		if s.Headers == nil {
			s.Headers = map[string]string{}
		}
		s.Headers[headerName] = value
	})
	return Save(cfg)
}

// UnsetServerHeader removes a custom header from the named server and persists.
// Missing server or missing header is a no-op (the post-condition — header
// absent — already holds).
func UnsetServerHeader(name, headerName string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	s, ok := cfg.GetServer(name)
	if !ok {
		return fmt.Errorf("server %q not found", name)
	}
	if s.Headers == nil {
		return nil
	}
	delete(s.Headers, headerName)
	if len(s.Headers) == 0 {
		// Drop the empty map so it round-trips out of the YAML (omitempty).
		s.Headers = nil
	}
	return Save(cfg)
}

// isHeaderTokenChar reports whether r is allowed in an HTTP field-name per
// RFC 7230 §3.2.6 (token). Used to reject malformed / injection-prone names.
func isHeaderTokenChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}

// validateHeader checks a custom header name/value pair. The name must be a
// non-empty RFC 7230 token; the value must not embed CR or LF (which would let
// a config value inject additional headers or split the request).
func validateHeader(name, value string) error {
	if name == "" {
		return fmt.Errorf("header name must not be empty")
	}
	for _, r := range name {
		if !isHeaderTokenChar(r) {
			return fmt.Errorf("invalid character %q in header name %q (must be a valid HTTP token)", r, name)
		}
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("header %q value must not contain CR or LF", name)
	}
	return nil
}

// ValidateHeader is the exported guard for callers outside this package (e.g.
// getClient, after ${ENV} expansion). Same rules as the internal check.
func ValidateHeader(name, value string) error { return validateHeader(name, value) }

// ExpandEnvHeaderValue expands $VAR / ${VAR} references in a header value using
// the environment, returning an error that NAMES the first referenced variable
// that is not set. This is stricter than os.ExpandEnv on purpose:
//
//   - A reference to an UNSET variable is an error, not a silent empty string.
//     The whole point of custom headers is to satisfy an authenticating proxy;
//     a forgotten `export` or a typo'd var name must fail loudly here rather
//     than send an empty `CF-Access-Client-Secret:` and bounce at the edge with
//     an opaque 403. A variable that is SET but empty (`export X=`) is honored
//     as an intentional empty value.
//   - `$$` is an escape for a literal `$`, so a value that legitimately
//     contains `$` (e.g. a token) can be written `pa$$word` without being
//     mangled into `pa` + an env lookup.
//
// The error never contains the resolved value (only the variable name), so it
// is safe to surface to the user / logs.
func ExpandEnvHeaderValue(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// `$$` → literal `$`.
		if i+1 < len(s) && s[i+1] == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}
		name, width := envRefName(s[i+1:])
		if name == "" {
			// Lone `$` or unparseable reference: keep the `$` literal.
			b.WriteByte('$')
			i++
			continue
		}
		val, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("environment variable %q is not set", name)
		}
		b.WriteString(val)
		i += 1 + width
	}
	return b.String(), nil
}

// envRefName parses the variable name immediately following a `$`. It accepts
// `{VAR}` (braced) and bare `VAR` (a run of [A-Za-z0-9_]). It returns the name
// and how many bytes it consumed (excluding the leading `$`); ("", 0) means the
// `$` does not introduce a valid reference.
func envRefName(s string) (name string, width int) {
	if len(s) == 0 {
		return "", 0
	}
	if s[0] == '{' {
		end := strings.IndexByte(s, '}')
		if end <= 1 { // no closing brace, or empty `${}`
			return "", 0
		}
		return s[1:end], end + 1
	}
	var j int
	for j < len(s) && isEnvNameByte(s[j]) {
		j++
	}
	return s[:j], j
}

func isEnvNameByte(c byte) bool {
	return c == '_' ||
		('a' <= c && c <= 'z') ||
		('A' <= c && c <= 'Z') ||
		('0' <= c && c <= '9')
}

// SetDefaultServer marks an existing server as the default and persists.
func SetDefaultServer(name string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	if _, ok := cfg.GetServer(name); !ok {
		return fmt.Errorf("server %q not found; configured servers:\n  - %s", name, strings.Join(cfg.serverNames(), "\n  - "))
	}
	cfg.DefaultServer = name
	return Save(cfg)
}

// RemoveServer deletes the named server and persists. If the removed server
// was the default and others remain, the default is reassigned to the first
// remaining server and its name is returned in reassignedTo. Removing the
// last server leaves none — the next Load() re-seeds the localhost default.
func RemoveServer(name string) (reassignedTo string, err error) {
	cfg, err := Load()
	if err != nil {
		return "", err
	}
	if _, ok := cfg.GetServer(name); !ok {
		return "", fmt.Errorf("server %q not found", name)
	}
	kept := make([]ServerEntry, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		if s.Name != name {
			kept = append(kept, s)
		}
	}
	cfg.Servers = kept
	if cfg.DefaultServer == name {
		if len(kept) > 0 {
			cfg.DefaultServer = kept[0].Name
			reassignedTo = kept[0].Name
		} else {
			cfg.DefaultServer = ""
		}
	}
	return reassignedTo, Save(cfg)
}

// AddProject adds a project to the config.
func AddProject(path string, autoWatch bool) error {
	cfg, err := Load()
	if err != nil {
		return err
	}

	for _, p := range cfg.Projects {
		if p.Path == path {
			if p.AutoWatch != autoWatch {
				p.AutoWatch = autoWatch
				return Save(cfg)
			}
			return nil
		}
	}

	cfg.Projects = append(cfg.Projects, ProjectEntry{
		Path:      path,
		AutoWatch: autoWatch,
	})

	return Save(cfg)
}

// RemoveProject removes a project from the config.
func RemoveProject(path string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}

	newProjects := make([]ProjectEntry, 0, len(cfg.Projects))
	for _, p := range cfg.Projects {
		if p.Path != path {
			newProjects = append(newProjects, p)
		}
	}

	cfg.Projects = newProjects
	return Save(cfg)
}

// GetLogsDir returns the logs directory path.
func GetLogsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	logsDir := filepath.Join(home, ".cix", "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return "", err
	}

	return logsDir, nil
}

// GetPIDFile returns the PID file path.
func GetPIDFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	pidDir := filepath.Join(home, ".cix")
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		return "", err
	}

	return filepath.Join(pidDir, "watcher.pid"), nil
}
