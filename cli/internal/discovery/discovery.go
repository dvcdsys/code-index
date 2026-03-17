package discovery

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DiscoveredFile represents a file found during discovery.
type DiscoveredFile struct {
	Path        string // absolute path
	RelPath     string // relative to project root
	Size        int64
	ContentHash string // SHA-256
	Language    string // detected from extension, may be empty
}

// Options configures file discovery behavior.
type Options struct {
	MaxFileSize int64    // skip files larger than this (default 524288 = 512KB)
	ExcludeDirs []string // additional directory names to exclude
}

// DefaultExcludeDirs are directories always excluded from discovery.
var DefaultExcludeDirs = map[string]bool{
	"node_modules": true, ".git": true, ".venv": true, "__pycache__": true,
	"dist": true, "build": true, ".next": true, ".cache": true, ".DS_Store": true,
	".idea": true, ".vscode": true, "vendor": true, "target": true, ".svn": true,
	".hg": true, "coverage": true, ".nyc_output": true, ".tox": true,
	".eggs": true, ".gradle": true, ".mvn": true,
}

// Discover walks the directory tree and returns discovered files.
func Discover(root string, opts Options) ([]DiscoveredFile, error) {
	if opts.MaxFileSize <= 0 {
		opts.MaxFileSize = 524288
	}

	excludeDirs := make(map[string]bool)
	for k, v := range DefaultExcludeDirs {
		excludeDirs[k] = v
	}
	for _, d := range opts.ExcludeDirs {
		excludeDirs[d] = true
	}

	// Load .gitignore patterns
	gitignorePatterns := loadGitignore(filepath.Join(root, ".gitignore"))

	var files []DiscoveredFile

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsPermission(err) {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return err
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}

		// Skip excluded directories
		if d.IsDir() {
			name := d.Name()
			if excludeDirs[name] {
				return filepath.SkipDir
			}
			// Skip hidden directories (except root)
			if path != root && strings.HasPrefix(name, ".") && name != "." {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip by .gitignore
		if matchesGitignore(gitignorePatterns, rel) {
			return nil
		}

		// Get file info
		info, err := d.Info()
		if err != nil {
			return nil
		}

		// Skip empty or oversized files
		size := info.Size()
		if size == 0 || size > opts.MaxFileSize {
			return nil
		}

		// Skip obvious binary extensions before reading
		if isBinaryExtension(path) {
			return nil
		}

		// Hash file and check for binary content
		hash, isBinary, err := hashFile(path)
		if err != nil {
			return nil // skip unreadable files
		}
		if isBinary {
			return nil
		}

		files = append(files, DiscoveredFile{
			Path:        path,
			RelPath:     rel,
			Size:        size,
			ContentHash: hash,
			Language:    DetectLanguage(path),
		})

		return nil
	})

	return files, err
}

// hashFile computes SHA-256 hash and detects binary content.
// Returns (hash, isBinary, error).
func hashFile(path string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, 8192)
	firstChunk := true

	for {
		n, err := f.Read(buf)
		if n > 0 {
			// Check first chunk for binary content (null bytes)
			if firstChunk {
				firstChunk = false
				for i := 0; i < n; i++ {
					if buf[i] == 0 {
						return "", true, nil
					}
				}
			}
			h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", false, err
		}
	}

	return hex.EncodeToString(h.Sum(nil)), false, nil
}

// binaryExts lists common binary file extensions to skip before reading content.
var binaryExts = map[string]bool{
	".pyc": true, ".pyo": true, ".class": true, ".o": true, ".obj": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".a": true, ".lib": true,
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".ico": true, ".svg": true,
	".bmp": true, ".tiff": true, ".webp": true,
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wav": true, ".flac": true,
	".zip": true, ".tar": true, ".gz": true, ".rar": true, ".7z": true, ".bz2": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
	".db": true, ".sqlite": true, ".sqlite3": true,
	".wasm": true, ".map": true,
}

func isBinaryExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return binaryExts[ext]
}

// --- Simple .gitignore support ---

type gitignorePattern struct {
	pattern string
	negated bool
	dirOnly bool
}

func loadGitignore(path string) []gitignorePattern {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var patterns []gitignorePattern
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		p := gitignorePattern{}

		if strings.HasPrefix(line, "!") {
			p.negated = true
			line = line[1:]
		}

		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}

		p.pattern = line
		patterns = append(patterns, p)
	}

	return patterns
}

func matchesGitignore(patterns []gitignorePattern, relPath string) bool {
	if len(patterns) == 0 {
		return false
	}

	matched := false
	// Normalize to forward slashes for matching
	relPath = filepath.ToSlash(relPath)

	for _, p := range patterns {
		var doesMatch bool
		pattern := p.pattern

		if strings.Contains(pattern, "/") {
			// Path pattern — match against full path
			doesMatch, _ = filepath.Match(pattern, relPath)
			if !doesMatch {
				// Try prefix match for directory patterns
				doesMatch = strings.HasPrefix(relPath, pattern+"/") || relPath == pattern
			}
		} else {
			// Name pattern — match against any path component
			parts := strings.Split(relPath, "/")
			for _, part := range parts {
				if m, _ := filepath.Match(pattern, part); m {
					doesMatch = true
					break
				}
			}
		}

		if doesMatch {
			if p.negated {
				matched = false
			} else {
				matched = true
			}
		}
	}

	return matched
}
