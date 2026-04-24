package langdetect

import "testing"

func TestDetect(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"app.py", "python"},
		{"index.ts", "typescript"},
		{"index.tsx", "typescript"},
		{"app.js", "javascript"},
		{"lib.rs", "rust"},
		{"Hello.java", "java"},
		{"util.c", "c"},
		{"util.h", "c"},
		{"lib.cpp", "cpp"},
		{"lib.cc", "cpp"},
		{"Makefile", "make"},
		{"GNUmakefile", "make"},
		{"Dockerfile", "dockerfile"},
		{"CMakeLists.txt", "cmake"},
		{"main.rb", "ruby"},
		{"style.css", "css"},
		{"config.yaml", "yaml"},
		{"config.yml", "yaml"},
		{"data.json", "json"},
		{"schema.graphql", "graphql"},
		{"schema.gql", "graphql"},
		{"main.tf", "hcl"},
		{"README.md", "markdown"},
		{"unknown.xyz", ""},
		{"/some/path/to/main.go", "go"},
		{"script.R", "r"},  // uppercase .R
		{"script.sh", "bash"},
	}
	for _, c := range cases {
		got := Detect(c.path)
		if got != c.want {
			t.Errorf("Detect(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
