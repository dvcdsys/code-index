//go:build ignore

// seedfixture writes a small, self-contained cix data directory for manually
// exercising the admin Resources screen: a few real vector collections, some
// cloned-checkout directories, and nothing else. Run it, point a server at the
// directory, then register only SOME of the projects so the rest show up as
// reclaimable.
//
// Build-tagged `ignore` so it never joins the module build; run it with
// `go run cmd/seedfixture/main.go <dir>`.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: seedfixture <data-dir>")
		os.Exit(2)
	}
	dir := os.Args[1]

	ns := filepath.Join(dir, "vectors", "ollama", "awhiteside_coderankembed_q8_0_gguf")
	store, err := vectorstore.Open(ns)
	check(err)
	defer store.Close()

	// Three collections. Only the first is registered as a project later, so
	// the other two are orphans.
	paths := []string{"/live/project", "/deleted/analytics-service", "/deleted/old-prototype"}
	sizes := []int{40, 900, 300}
	for i, p := range paths {
		chunks := make([]vectorstore.Chunk, sizes[i])
		embs := make([][]float32, sizes[i])
		for j := range chunks {
			chunks[j] = vectorstore.Chunk{
				Content:   fmt.Sprintf("func handler%d() error { return nil }", j),
				FilePath:  fmt.Sprintf("pkg/mod%d.go", j%20),
				StartLine: j, EndLine: j + 8,
				ChunkType: "function", SymbolName: fmt.Sprintf("handler%d", j), Language: "go",
			}
			embs[j] = make([]float32, 768)
			embs[j][j%768] = 1
		}
		check(store.UpsertChunks(context.Background(), p, chunks, embs))
		size, _ := store.CollectionSizeBytes(p)
		fmt.Printf("collection %-32s %d docs, %d bytes -> %s\n", p, sizes[i], size, store.DBPath())
	}

	// Cloned checkouts: one for the live project, two for projects that are
	// gone, plus a legacy UUID-named directory from an older layout.
	repos := filepath.Join(dir, "repos", "repos")
	for _, name := range []string{
		projects.HashPath("/live/project"),
		projects.HashPath("/deleted/analytics-service"),
		"3f0c1a9e2b7d4c58",
		"ecc2afe1-3a8d-4fbd-946e-dc6985205d7c",
	} {
		d := filepath.Join(repos, name)
		check(os.MkdirAll(filepath.Join(d, ".git"), 0o755))
		check(os.WriteFile(filepath.Join(d, ".git", "config"),
			[]byte("[remote \"origin\"]\n\turl = https://example.test/acme/"+name[:6]+".git\n"), 0o644))
		check(os.WriteFile(filepath.Join(d, "payload.bin"), make([]byte, 3<<20), 0o644))
	}

	// An abandoned namespace from a previous embedding model.
	old := filepath.Join(dir, "chroma", "ollama", "legacy-embed-model", "aabbccdd")
	check(os.MkdirAll(old, 0o755))
	check(os.WriteFile(filepath.Join(old, "doc.gob"), make([]byte, 5<<20), 0o644))

	fmt.Println("seeded", dir)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}
