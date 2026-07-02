package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Twin of the server goldens in
// server/internal/httpapi/file_contract_test.go (wantFileContentWire /
// wantDirectoryListingWire). These are the exact bytes the server emits; the
// modules share no code, so keeping them byte-identical is how CLI↔server wire
// compatibility is pinned. If the server test's golden changes, change these too
// and adjust the structs in file.go.
const (
	wantFileContentWire      = `{"content":"L1\nL2","end_line":2,"file_path":"internal/x.go","language":"go","start_line":1,"total_lines":2,"truncated":false}`
	wantDirectoryListingWire = `{"dir":"internal","entries":[{"name":"sub","type":"dir"},{"language":"go","name":"x.go","size":42,"type":"file"}],"truncated":false}`
)

// TestReadFile_ParsesServerContract feeds the CLI client the exact JSON the
// server emits and asserts every field lands in the right place — the core of
// CLI/MCP compatibility, since the MCP cix_file tool shares this ReadFile path.
func TestReadFile_ParsesServerContract(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, wantFileContentWire)
	}))
	defer srv.Close()

	fc, err := New(srv.URL, "k").ReadFile("github.com/o/r@main", "internal/x.go", 1, 2)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if fc.FilePath != "internal/x.go" || fc.StartLine != 1 || fc.EndLine != 2 ||
		fc.TotalLines != 2 || fc.Truncated || fc.Content != "L1\nL2" {
		t.Errorf("parsed FileContent mismatch: %+v", fc)
	}
	if fc.Language == nil || *fc.Language != "go" {
		t.Errorf("language = %v, want \"go\"", fc.Language)
	}
	if !strings.HasSuffix(gotPath, "/file") {
		t.Errorf("path = %q, want a .../file POST", gotPath)
	}
	if gotBody["file"] != "internal/x.go" {
		t.Errorf("request body file = %v", gotBody["file"])
	}
	if gotBody["start"] != float64(1) || gotBody["end"] != float64(2) {
		t.Errorf("request body start/end = %v/%v, want 1/2", gotBody["start"], gotBody["end"])
	}
}

// TestReadFile_OmitsUnsetRange is a wire-contract guard: with no range the
// client must send neither start nor end (whole-file read), never a 0 that the
// server would reject or misread.
func TestReadFile_OmitsUnsetRange(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, wantFileContentWire)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "k").ReadFile("p", "f.go", 0, 0); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if _, ok := gotBody["start"]; ok {
		t.Errorf("start must be omitted when 0, body=%v", gotBody)
	}
	if _, ok := gotBody["end"]; ok {
		t.Errorf("end must be omitted when 0, body=%v", gotBody)
	}
}

// TestReadFile_SurfacesServerErrors covers the new/existing status codes the
// server returns for these endpoints — the client must turn each into an error
// carrying the server's detail, not silently succeed.
func TestReadFile_SurfacesServerErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		detail     string
		wantSubstr string
	}{
		{"inverted range 422", http.StatusUnprocessableEntity, "end must be >= start", "end must be >= start"},
		{"local project 409", http.StatusConflict, "this project is local", "this project is local"},
		{"not found 404", http.StatusNotFound, "file not found", "file not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"detail": tc.detail})
			}))
			defer srv.Close()

			_, err := New(srv.URL, "k").ReadFile("p", "f.go", 4, 2)
			if err == nil {
				t.Fatalf("expected an error for status %d", tc.status)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// TestListDir_ParsesServerContract mirrors the above for the tree endpoint.
func TestListDir_ParsesServerContract(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, wantDirectoryListingWire)
	}))
	defer srv.Close()

	dl, err := New(srv.URL, "k").ListDir("github.com/o/r@main", "internal")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/tree") {
		t.Errorf("path = %q, want a .../tree POST", gotPath)
	}
	if dl.Dir != "internal" || dl.Truncated || len(dl.Entries) != 2 {
		t.Fatalf("listing mismatch: %+v", dl)
	}
	sub, file := dl.Entries[0], dl.Entries[1]
	if sub.Name != "sub" || sub.Type != "dir" || sub.Size != nil {
		t.Errorf("dir entry mismatch: %+v", sub)
	}
	if file.Name != "x.go" || file.Type != "file" || file.Size == nil || *file.Size != 42 {
		t.Errorf("file entry mismatch: %+v", file)
	}
	if file.Language == nil || *file.Language != "go" {
		t.Errorf("file language = %v, want \"go\"", file.Language)
	}
}

// TestListDir_SurfacesServerErrors covers the tree endpoint's malformed-body 400
// and the not-found 404.
func TestListDir_SurfacesServerErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		detail string
	}{
		{"malformed body 400", http.StatusBadRequest, "invalid request body"},
		{"not found 404", http.StatusNotFound, "directory not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"detail": tc.detail})
			}))
			defer srv.Close()

			_, err := New(srv.URL, "k").ListDir("p", "internal")
			if err == nil || !strings.Contains(err.Error(), tc.detail) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.detail)
			}
		})
	}
}
