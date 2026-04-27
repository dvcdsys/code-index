package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	API      APIConfig      `yaml:"api"`
	Watcher  WatcherConfig  `yaml:"watcher"`
	Server   ServerConfig   `yaml:"server"`
	Indexing IndexingConfig `yaml:"indexing"`
	Projects []ProjectEntry `yaml:"projects"`
}

type APIConfig struct {
	URL string `yaml:"url"`
	Key string `yaml:"key"`
}

type WatcherConfig struct {
	Enabled          bool     `yaml:"enabled"`
	DebounceMS       int      `yaml:"debounce_ms"`
	ExcludePatterns  []string `yaml:"exclude"`
	SyncIntervalMins int      `yaml:"sync_interval_mins"`
}

type ServerConfig struct {
	Port     int `yaml:"port"`
	CacheTTL int `yaml:"cache_ttl"`
}

type IndexingConfig struct {
	BatchSize int `yaml:"batchsize"`

	// StreamingIdleTimeoutSec is the maximum allowed silence on the streaming
	// /index/files response before the CLI gives up and closes the conn. The
	// server emits a heartbeat every 10s, so 30s gives the network three
	// retry windows. Set to 0 to disable the watchdog (not recommended).
	StreamingIdleTimeoutSec int `yaml:"streaming_idle_timeout_sec"`
}

type ProjectEntry struct {
	Path      string `yaml:"path"`
	AutoWatch bool   `yaml:"auto_watch"`
}

var (
	globalConfig *Config
	configPath   string
)

// defaults returns a Config populated with default values.
func defaults() Config {
	return Config{
		API: APIConfig{
			URL: "http://localhost:21847",
		},
		Watcher: WatcherConfig{
			Enabled:    true,
			DebounceMS: 5000,
			ExcludePatterns: []string{
				"node_modules", ".git", ".venv", "__pycache__",
				"dist", "build", ".next", ".cache", ".DS_Store",
			},
			SyncIntervalMins: 5,
		},
		Server: ServerConfig{
			Port:     8080,
			CacheTTL: 300,
		},
		Indexing: IndexingConfig{
			BatchSize:               20,
			StreamingIdleTimeoutSec: 30,
		},
	}
}

// normalizeLegacyKeys maps old viper-generated YAML key names to the current
// yaml struct tag names. Provides backward compatibility for configs created
// before the viper→yaml.v3 migration.
func normalizeLegacyKeys(data []byte) []byte {
	for _, pair := range [][2]string{
		{"debouncems:", "debounce_ms:"},
		{"excludepatterns:", "exclude:"},
		{"cachettl:", "cache_ttl:"},
		{"autowatch:", "auto_watch:"},
	} {
		data = bytes.ReplaceAll(data, []byte(pair[0]), []byte(pair[1]))
	}
	return data
}

// Load loads configuration from ~/.cix/config.yaml.
// Fields absent from the file keep their default values.
func Load() (*Config, error) {
	if globalConfig != nil {
		return globalConfig, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}

	configDir := filepath.Join(home, ".cix")
	configPath = filepath.Join(configDir, "config.yaml")

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	cfg := defaults()

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			globalConfig = &cfg
			return globalConfig, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	normalized := normalizeLegacyKeys(data)
	if err := yaml.Unmarshal(normalized, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	globalConfig = &cfg

	// If the file used legacy viper-style keys, re-save in the current format.
	if !bytes.Equal(data, normalized) {
		_ = Save(&cfg)
	}

	return globalConfig, nil
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