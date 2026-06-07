// Command stability feeds adversarial inputs to the WASM backend and shows the
// host survives every one, and that a hard resource-limit fault surfaces as a
// recoverable Go error rather than a process crash — the crash-isolation
// property that cgo cannot offer (a C segfault/abort kills the whole process).
//
//	go run ./cmd/stability
package main

import (
	"context"
	"fmt"
	"strings"

	wasmts "github.com/dvcdsys/code-index/poc/wasm-treesitter"
)

func main() {
	ctx := context.Background()
	eng, err := wasmts.New(ctx, 0)
	if err != nil {
		panic(err)
	}
	defer eng.Close()

	random := make([]byte, 200000)
	for i := range random {
		random[i] = byte(i*37 + 11)
	}
	adversarial := []struct {
		name string
		src  []byte
	}{
		{"deeply nested [ ] x100000", []byte(strings.Repeat("[", 100000) + strings.Repeat("]", 100000))},
		{"deeply nested ( x200000", []byte(strings.Repeat("(", 200000))},
		{"deep ternary x50000", []byte("let x=" + strings.Repeat("a?b:", 50000) + "c")},
		{"huge single token 5MB", []byte(strings.Repeat("z", 5_000_000))},
		{"invalid UTF-8 / random 200KB", random},
		{"unbalanced template `${ x80000", []byte(strings.Repeat("`${", 80000))},
	}

	fmt.Println("=== STABILITY: adversarial inputs — host must survive every one ===")
	survived := 0
	for _, a := range adversarial {
		r, err := eng.Parse("tree_sitter_typescript", a.src)
		status := fmt.Sprintf("parsed-ok (nodes=%d, hasError=%v)", r.Nodes, r.HasError)
		if err != nil {
			status = "TRAP CONTAINED → " + err.Error()
		}
		fmt.Printf("  %-30s (%7d B): host ALIVE, %s\n", a.name, len(a.src), status)
		survived++
	}
	fmt.Printf("\n  → host survived ALL %d adversarial inputs.\n", survived)

	if r, err := eng.Parse("tree_sitter_typescript", []byte("function f(x: number) { return x + 1; }")); err == nil {
		fmt.Printf("  → normal file after barrage: parsed-ok (nodes=%d) — host fully functional\n", r.Nodes)
	}

	// Hard resource containment: a memory-capped instance turns an over-limit
	// parse into a Go error, not a host crash.
	capped, err := wasmts.New(ctx, 24) // ~1.5 MB cap, below the module's static needs
	if err != nil {
		fmt.Printf("  → memory-capped (1.5MB) instance: over-limit surfaced as a Go error (contained): %s\n", firstline(err.Error()))
	} else {
		_, perr := capped.Parse("tree_sitter_typescript", []byte(strings.Repeat("const x=[1,2,3];\n", 50000)))
		fmt.Printf("  → memory-capped big parse: contained err=%v — host ALIVE\n", perr != nil)
		capped.Close()
	}

	fmt.Println("\n  Contrast: under cgo, an equivalent guest fault (C stack overflow / OOM abort)\n  is a native SIGSEGV/abort that kills the whole cix-server process.")
}

func firstline(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
