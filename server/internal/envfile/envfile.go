// Package envfile loads a KEY='value' file into the process environment.
//
// It exists so a supervisor that cannot (or should not) run a shell wrapper can
// still hand cix-server its configuration. On macOS the launcher generates a
// bash wrapper that does `set -a; source server.env`; on Windows there is no
// such shell, so the scheduled task execs cix-server.exe directly with
// `-env-file <path>` and the server reads the same file itself.
//
// The dialect is exactly what the desktop launcher writes (cli/launcher
// writeServerEnv) and reads back (readEnvFile), so one file serves both:
//
//   - one KEY=value per line; blank lines and lines starting with # are ignored
//   - an optional leading `export `
//   - the value may be wrapped in matching single or double quotes, which are
//     stripped; inside single quotes the writer escapes an embedded quote as
//     '\” and that sequence is undone here
//
// Deliberately not a shell: no variable expansion, no command substitution, no
// multi-line values. A line that is not KEY=value is skipped, not an error.
package envfile

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Parse reads path into a map without touching the environment.
func Parse(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	vars := map[string]string{}
	sc := bufio.NewScanner(f)
	// A line is an API key or a path; the default 64 KiB token limit is plenty,
	// but raise it so a pathological value fails loudly rather than silently
	// truncating the rest of the file.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			// A file saved by a Windows editor may carry a UTF-8 BOM.
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(key), "export "))
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '\'' && value[len(value)-1] == '\'') ||
				(value[0] == '"' && value[len(value)-1] == '"') {
				value = value[1 : len(value)-1]
			}
		}
		vars[key] = strings.ReplaceAll(value, `'\''`, `'`)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return vars, nil
}

// Load reads path and exports each variable that is not already set.
//
// Precedence: the real environment wins. A variable present in the process
// environment — even an empty one — is left alone, so an operator can override
// a single setting for one run (`$env:CIX_PORT=…; cix-server -env-file …`)
// without editing the file, and so a value injected by a service manager is
// never silently replaced by a stale file. The file fills in what is missing.
//
// It returns the keys it actually set.
func Load(path string) ([]string, error) {
	vars, err := Parse(path)
	if err != nil {
		return nil, fmt.Errorf("env file: %w", err)
	}
	var set []string
	for k, v := range vars {
		if _, present := os.LookupEnv(k); present {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return set, fmt.Errorf("env file: set %s: %w", k, err)
		}
		set = append(set, k)
	}
	return set, nil
}
