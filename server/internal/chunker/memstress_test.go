//go:build unix

package chunker

// Temporary memory-stress harness for the wasm tree-sitter backend.
// Run explicitly:
//   go test ./internal/chunker -run TestMemStress -v -timeout 30m
// Not part of normal CI (guarded by CIX_MEMSTRESS).

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/chunker/tswasm"
)

func langForExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js":
		return "javascript"
	case ".py":
		return "python"
	case ".md":
		return "markdown"
	case ".css":
		return "css"
	case ".sh":
		return "bash"
	case ".c", ".h":
		return "c"
	default:
		return ""
	}
}

func rssMB() float64 {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	// darwin: bytes; linux: KiB
	if runtime.GOOS == "darwin" {
		return float64(ru.Maxrss) / (1 << 20)
	}
	return float64(ru.Maxrss) / 1024
}

func TestMemStress(t *testing.T) {
	if os.Getenv("CIX_MEMSTRESS") == "" {
		t.Skip("set CIX_MEMSTRESS=1 to run")
	}
	root := os.Getenv("CIX_MEMSTRESS_ROOT")
	if root == "" {
		root = "../../.."
	}

	type f struct {
		path, lang, content string
	}
	var files []f
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "dist" || name == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}
		lang := langForExt(p)
		if lang == "" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil || len(b) == 0 || len(b) > 512*1024 {
			return nil
		}
		files = append(files, f{p, lang, string(b)})
		return nil
	})
	t.Logf("collected %d files from %s", len(files), root)

	report := func(tag string, n int) {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		ps := tswasm.PoolStats()
		t.Logf("%s n=%d heapAlloc=%.1fMB heapSys=%.1fMB sys=%.1fMB numGC=%d maxRSS=%.1fMB | pool: created=%d closed=%d recycled=%d live=%d idle=%d idleMem=%.1fMB",
			tag, n,
			float64(ms.HeapAlloc)/(1<<20), float64(ms.HeapSys)/(1<<20), float64(ms.Sys)/(1<<20), ms.NumGC, rssMB(),
			ps.Created, ps.Closed, ps.Recycled, ps.LiveOpen, ps.IdlePooled, float64(ps.IdleMemBytes)/(1<<20))
	}

	report("start", 0)
	total := 0
	for pass := 1; pass <= 4; pass++ {
		for _, fl := range files {
			chunks, refs, err := ChunkFile(fl.path, fl.content, fl.lang, 0)
			if err != nil {
				t.Fatalf("chunk %s: %v", fl.path, err)
			}
			_ = chunks
			_ = refs
			total++
			if total%500 == 0 {
				report("  tick", total)
			}
		}
		report(fmt.Sprintf("pass %d done", pass), total)
	}
	report("end", total)
}
