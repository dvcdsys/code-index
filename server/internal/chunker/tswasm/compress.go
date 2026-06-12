//go:build ignore

// compress.go brotli-compresses the raw ts-core.wasm (produced by build.sh) into
// the committed ts-core.wasm.br that tswasm.go embeds. The raw .wasm is a build
// intermediate (gitignored); only the ~10x-smaller .br is committed, so a server
// build needs neither zig nor the 56 MB blob in git.
//
//	go run compress.go        # invoked as the last step of build.sh
package main

import (
	"log"
	"os"

	"github.com/andybalholm/brotli"
)

func main() {
	raw, err := os.ReadFile("ts-core.wasm")
	if err != nil {
		log.Fatalf("read ts-core.wasm (run build.sh first): %v", err)
	}
	out, err := os.Create("ts-core.wasm.br")
	if err != nil {
		log.Fatal(err)
	}
	w := brotli.NewWriterLevel(out, brotli.BestCompression)
	if _, err := w.Write(raw); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}
	if err := out.Close(); err != nil {
		log.Fatal(err)
	}
	fi, _ := os.Stat("ts-core.wasm.br")
	log.Printf("compressed %d → ts-core.wasm.br (%d bytes)", len(raw), fi.Size())
}
