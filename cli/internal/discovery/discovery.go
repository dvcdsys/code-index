package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
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

	// gitignoreMatchers holds a stack of matchers keyed by directory depth.
	// Each entry corresponds to a .gitignore found at that directory level.
	type gitignoreEntry struct {
		dir     string // absolute path of the directory containing .gitignore
		matcher *ignore.GitIgnore
	}
	var gitignoreStack []gitignoreEntry

	// Load root .gitignore if present
	if gi, err := ignore.CompileIgnoreFile(filepath.Join(root, ".gitignore")); err == nil {
		gitignoreStack = append(gitignoreStack, gitignoreEntry{dir: root, matcher: gi})
	}

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

			// Pop gitignore entries that are no longer ancestors of current dir
			for len(gitignoreStack) > 0 {
				top := gitignoreStack[len(gitignoreStack)-1]
				if top.dir == root || strings.HasPrefix(path, top.dir+string(filepath.Separator)) {
					break
				}
				gitignoreStack = gitignoreStack[:len(gitignoreStack)-1]
			}

			// Check for .gitignore in this directory (skip root, already loaded)
			if path != root {
				giPath := filepath.Join(path, ".gitignore")
				if gi, err := ignore.CompileIgnoreFile(giPath); err == nil {
					gitignoreStack = append(gitignoreStack, gitignoreEntry{dir: path, matcher: gi})
				}
			}

			// Check if this directory itself is ignored by any gitignore in the stack
			if path != root {
				for _, entry := range gitignoreStack {
					entryRel, err := filepath.Rel(entry.dir, path)
					if err != nil {
						continue
					}
					if entry.matcher.MatchesPath(entryRel + "/") {
						return filepath.SkipDir
					}
				}
			}

			return nil
		}

		// Skip by .gitignore — check all matchers in the stack
		for _, entry := range gitignoreStack {
			entryRel, err := filepath.Rel(entry.dir, path)
			if err != nil {
				continue
			}
			if entry.matcher.MatchesPath(entryRel) {
				return nil
			}
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

