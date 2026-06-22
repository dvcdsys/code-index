package wasmts

import (
	"context"
	"strings"
	"testing"
)

// findDecls returns the source text of every node whose kind is in want.
func findDecls(src []byte, nodes []Node, want map[string]bool) map[string][]string {
	out := map[string][]string{}
	for _, n := range nodes {
		if want[n.Kind] {
			txt := string(src[n.StartByte:n.EndByte])
			if i := strings.IndexByte(txt, '\n'); i >= 0 {
				txt = txt[:i]
			}
			out[n.Kind] = append(out[n.Kind], txt)
		}
	}
	return out
}

func TestParseNodes_MultiLanguage(t *testing.T) {
	eng, err := New(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	cases := []struct {
		export string
		src    string
		want   []string // kinds that MUST appear
	}{
		{
			"tree_sitter_go",
			"package main\n\nfunc Hello() int { return 1 }\n\ntype T struct{ X int }\n",
			[]string{"function_declaration", "type_declaration"},
		},
		{
			"tree_sitter_python",
			"def hello():\n    return 1\n\nclass C:\n    def m(self):\n        pass\n",
			[]string{"function_definition", "class_definition"},
		},
		{
			"tree_sitter_typescript",
			"export function f(x: number): number { return x }\nclass C { m() {} }\ninterface I { a: number }\n",
			[]string{"function_declaration", "class_declaration", "interface_declaration"},
		},
	}

	for _, c := range cases {
		t.Run(c.export, func(t *testing.T) {
			nodes, err := eng.ParseNodes(c.export, []byte(c.src))
			if err != nil {
				t.Fatal(err)
			}
			if len(nodes) == 0 {
				t.Fatal("no nodes")
			}
			// no parse errors on valid input
			errs := 0
			for _, n := range nodes {
				if n.Error || n.Missing {
					errs++
				}
			}
			if errs != 0 {
				t.Errorf("expected 0 error/missing nodes, got %d", errs)
			}
			want := map[string]bool{}
			for _, k := range c.want {
				want[k] = true
			}
			got := findDecls([]byte(c.src), nodes, want)
			for _, k := range c.want {
				if len(got[k]) == 0 {
					t.Errorf("kind %q not found", k)
				}
			}
			t.Logf("%s: %d nodes, decls=%v", c.export, len(nodes), got)
		})
	}
}
