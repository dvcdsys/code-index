package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"

	"github.com/anthropics/code-index/cli/internal/projectconfig"
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

	// ignoreMatchers holds a stack of matchers keyed by directory depth.
	// Each entry corresponds to a .gitignore, .cixignore, or submodule rule.
	type ignoreEntry struct {
		dir     string // absolute path of the directory containing the ignore file
		matcher *ignore.GitIgnore
	}
	var ignoreStack []ignoreEntry

	// Load root .gitignore and .cixignore if present (same format)
	for _, ignoreFile := range []string{".gitignore", ".cixignore"} {
		if gi, err := ignore.CompileIgnoreFile(filepath.Join(root, ignoreFile)); err == nil {
			ignoreStack = append(ignoreStack, ignoreEntry{dir: root, matcher: gi})
		}
	}

	// Load .cixconfig.yaml — if ignore.submodules is true, exclude submodule paths
	if projCfg, err := projectconfig.Load(root); err == nil && projCfg.Ignore.Submodules {
		if subPaths, err := projectconfig.SubmodulePaths(root); err == nil && len(subPaths) > 0 {
			patterns := make([]string, len(subPaths))
			for i, sp := range subPaths {
				patterns[i] = sp + "/"
			}
			gi := ignore.CompileIgnoreLines(patterns...)
			ignoreStack = append(ignoreStack, ignoreEntry{dir: root, matcher: gi})
		}
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

		// Pop ignore entries that are no longer ancestors of current path
		for len(ignoreStack) > 0 {
			top := ignoreStack[len(ignoreStack)-1]
			if top.dir == root || strings.HasPrefix(path, top.dir+string(filepath.Separator)) {
				break
			}
			ignoreStack = ignoreStack[:len(ignoreStack)-1]
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

			// Check if this directory is ignored by any ignore rule in the stack
			if path != root {
				for _, entry := range ignoreStack {
					entryRel, err := filepath.Rel(entry.dir, path)
					if err != nil {
						continue
					}
					if entry.matcher.MatchesPath(entryRel + "/") {
						return filepath.SkipDir
					}
				}
			}

			// Check for .gitignore and .cixignore in this directory (skip root, already loaded)
			if path != root {
				for _, ignoreFile := range []string{".gitignore", ".cixignore"} {
					giPath := filepath.Join(path, ignoreFile)
					if gi, err := ignore.CompileIgnoreFile(giPath); err == nil {
						ignoreStack = append(ignoreStack, ignoreEntry{dir: path, matcher: gi})
					}
				}
			}

			return nil
		}

		// Skip ignore/config files themselves
		baseName := filepath.Base(path)
		if baseName == ".gitignore" || baseName == ".cixignore" || baseName == ".cixconfig.yaml" || baseName == ".gitmodules" {
			return nil
		}

		// Skip by ignore rules — check all matchers in the stack
		for _, entry := range ignoreStack {
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
// Uses http.DetectContentType on the first 512 bytes to determine if the file
// is binary (MIME type not starting with "text/").
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
			if firstChunk {
				firstChunk = false
				sniffSize := n
				if sniffSize > 512 {
					sniffSize = 512
				}
				mime := http.DetectContentType(buf[:sniffSize])
				if !strings.HasPrefix(mime, "text/") {
					return "", true, nil
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
	// Dependency lock/checksum files — extremely large token counts, no semantic value.
	".lock": true, ".sum": true,
	// Log files — large, ephemeral, no code value.
	".log": true,
}

func isBinaryExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return binaryExts[ext]
}

