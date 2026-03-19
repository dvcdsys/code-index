package projectconfig

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProjectConfig represents the per-project .cixconfig.yaml file.
type ProjectConfig struct {
	Ignore IgnoreConfig `yaml:"ignore"`
}

// IgnoreConfig controls which files/directories are excluded from indexing.
type IgnoreConfig struct {
	Submodules bool `yaml:"submodules"`
}

// Load reads .cixconfig.yaml from the project root.
// Returns a zero-value config (not an error) if the file does not exist.
func Load(projectRoot string) (ProjectConfig, error) {
	var cfg ProjectConfig

	data, err := os.ReadFile(filepath.Join(projectRoot, ".cixconfig.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}

	return cfg, nil
}

// SubmodulePaths parses .gitmodules and returns the list of submodule paths.
// Returns nil (not an error) if .gitmodules does not exist.
func SubmodulePaths(projectRoot string) ([]string, error) {
	f, err := os.Open(filepath.Join(projectRoot, ".gitmodules"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var paths []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "path") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				p := strings.TrimSpace(parts[1])
				if p != "" {
					paths = append(paths, p)
				}
			}
		}
	}

	return paths, scanner.Err()
}