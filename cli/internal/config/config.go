package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

type Config struct {
	API      APIConfig      `mapstructure:"api"`
	Watcher  WatcherConfig  `mapstructure:"watcher"`
	Server   ServerConfig   `mapstructure:"server"`
	Indexing IndexingConfig `mapstructure:"indexing"`
	Projects []ProjectEntry `mapstructure:"projects"`
}

type APIConfig struct {
	URL string `mapstructure:"url"`
	Key string `mapstructure:"key"`
}

type WatcherConfig struct {
	Enabled     bool     `mapstructure:"enabled"`
	DebounceMS  int      `mapstructure:"debounce_ms"`
	ExcludePatterns []string `mapstructure:"exclude"`
}

type ServerConfig struct {
	Port     int `mapstructure:"port"`
	CacheTTL int `mapstructure:"cache_ttl"`
}

type IndexingConfig struct {
	BatchSize int `mapstructure:"batchsize"`
}

type ProjectEntry struct {
	Path      string `mapstructure:"path"`
	AutoWatch bool   `mapstructure:"auto_watch"`
}

var (
	globalConfig *Config
	configPath   string
)

// Load loads configuration from ~/.cix/config.yaml
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

	// Create config dir if not exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	viper.SetConfigFile(configPath)
	viper.SetConfigType("yaml")

	// Set defaults
	viper.SetDefault("api.url", "http://localhost:21847")
	viper.SetDefault("watcher.enabled", true)
	viper.SetDefault("watcher.debounce_ms", 5000)
	viper.SetDefault("watcher.exclude", []string{
		"node_modules", ".git", ".venv", "__pycache__",
		"dist", "build", ".next", ".cache", ".DS_Store",
	})
	viper.SetDefault("server.port", 8080)
	viper.SetDefault("server.cache_ttl", 300)
	viper.SetDefault("indexing.batchsize", 20)

	// Read config file if exists, ignore if missing
	if err := viper.ReadInConfig(); err != nil {
		if !os.IsNotExist(err) {
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				return nil, fmt.Errorf("read config: %w", err)
			}
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	globalConfig = &cfg
	return globalConfig, nil
}

// Save saves the current configuration to disk
func Save(cfg *Config) error {
	viper.Set("api", cfg.API)
	viper.Set("watcher", cfg.Watcher)
	viper.Set("server", cfg.Server)
	viper.Set("indexing", cfg.Indexing)
	viper.Set("projects", cfg.Projects)

	if err := viper.WriteConfig(); err != nil {
		// Try to write if file doesn't exist
		if err := viper.SafeWriteConfig(); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
	}

	globalConfig = cfg
	return nil
}

// GetConfigPath returns the path to the config file
func GetConfigPath() string {
	return configPath
}

// AddProject adds a project to the config
func AddProject(path string, autoWatch bool) error {
	cfg, err := Load()
	if err != nil {
		return err
	}

	// Check if already exists
	for _, p := range cfg.Projects {
		if p.Path == path {
			// Update autoWatch if different
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

// RemoveProject removes a project from the config
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

// GetLogsDir returns the logs directory path
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

// GetPIDFile returns the PID file path
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
