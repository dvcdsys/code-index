// Command bench parses every .ts file under a directory with the WASM backend
// and reports throughput — apples-to-apples with the cgo/gotreesitter numbers
// in ../../README.md (same corpus, same full-tree walk).
//
//	go run ./cmd/bench /path/to/vscode/src/vs/editor
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	wasmts "github.com/dvcdsys/code-index/poc/wasm-treesitter"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: bench <dir-with-.ts-files>")
		os.Exit(2)
	}
	root := os.Args[1]
	ctx := context.Background()
	eng, err := wasmts.New(ctx, 0)
	if err != nil {
		panic(err)
	}
	defer eng.Close()

	var files []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e == nil && !d.IsDir() && filepath.Ext(p) == ".ts" {
			files = append(files, p)
		}
		return nil
	})
	fmt.Printf("corpus: %d .ts files\n\n", len(files))

	type res struct {
		path  string
		dur   time.Duration
		isErr bool
	}
	var all []res
	var totalParse time.Duration
	var totalBytes, errFiles, totalNodes int
	start := time.Now()
	for _, f := range files {
		src, _ := os.ReadFile(f)
		t0 := time.Now()
		r, err := eng.Parse("tree_sitter_typescript", src)
		d := time.Since(t0)
		if err != nil {
			fmt.Printf("  trap on %s: %v\n", filepath.Base(f), err)
			continue
		}
		totalParse += d
		totalBytes += len(src)
		totalNodes += r.Nodes
		if r.HasError {
			errFiles++
		}
		all = append(all, res{f, d, r.HasError})
	}
	wall := time.Since(start)
	sort.Slice(all, func(i, j int) bool { return all[i].dur > all[j].dur })
	mb := float64(totalBytes) / 1e6
	fmt.Println("=== WASM: official tree-sitter via wazero (pure-Go host, no cgo) ===")
	fmt.Printf("  wall: %v   parse+walk: %v\n", wall.Round(time.Millisecond), totalParse.Round(time.Millisecond))
	fmt.Printf("  throughput: %.0f files/s, %.1f MB/s\n", float64(len(files))/wall.Seconds(), mb/totalParse.Seconds())
	fmt.Printf("  ERROR trees: %d / %d   nodes walked: %d\n", errFiles, len(files), totalNodes)
	fmt.Printf("  slowest 5:\n")
	for i := 0; i < 5 && i < len(all); i++ {
		e := ""
		if all[i].isErr {
			e = " [ERROR]"
		}
		fmt.Printf("    %8v  %s%s\n", all[i].dur.Round(time.Millisecond), filepath.Base(all[i].path), e)
	}
}
