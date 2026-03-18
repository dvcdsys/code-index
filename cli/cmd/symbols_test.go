package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRunSymbols_Results(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	sig := "func HandleRequest(w http.ResponseWriter, r *http.Request)"

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/symbols"):
			writeJSON(w, 200, map[string]any{
				"results": []map[string]any{
					{
						"name":      "HandleRequest",
						"kind":      "function",
						"file_path": proj + "/handler.go",
						"line":      42,
						"end_line":  60,
						"language":  "go",
						"signature": sig,
					},
				},
				"total": 1,
			})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := symbolsProject
	defer func() { symbolsProject = old }()
	symbolsProject = proj
	symbolsLimit = 20
	symbolsKinds = nil

	out, err := captureOutput(func() error {
		return runSymbols(nil, []string{"HandleRequest"})
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "HandleRequest") {
		t.Errorf("expected symbol name in output, got:\n%s", out)
	}
	if !strings.Contains(out, "handler.go") {
		t.Errorf("expected file path in output, got:\n%s", out)
	}
	if !strings.Contains(out, "function") {
		t.Errorf("expected kind in output, got:\n%s", out)
	}
}

func TestRunSymbols_EmptyResults(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/symbols"):
			writeJSON(w, 200, map[string]any{"results": []any{}, "total": 0})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := symbolsProject
	defer func() { symbolsProject = old }()
	symbolsProject = proj
	symbolsLimit = 20
	symbolsKinds = nil

	out, err := captureOutput(func() error {
		return runSymbols(nil, []string{"NoSuchSymbol"})
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No symbols") {
		t.Errorf("expected 'No symbols', got:\n%s", out)
	}
}

func TestRunSymbols_WithKindFilter(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	var receivedKinds []string

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/symbols"):
			body, _ := io.ReadAll(r.Body)
			var req map[string]json.RawMessage
			_ = json.Unmarshal(body, &req)
			if raw, ok := req["kinds"]; ok {
				_ = json.Unmarshal(raw, &receivedKinds)
			}
			writeJSON(w, 200, map[string]any{
				"results": []map[string]any{
					{"name": "UserService", "kind": "class", "file_path": proj + "/service.go", "line": 1, "end_line": 50, "language": "go"},
				},
				"total": 1,
			})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := symbolsProject
	defer func() { symbolsProject = old }()
	symbolsProject = proj
	symbolsLimit = 20
	symbolsKinds = []string{"class"}

	out, err := captureOutput(func() error {
		return runSymbols(nil, []string{"UserService"})
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "UserService") {
		t.Errorf("expected symbol in output, got:\n%s", out)
	}
	if len(receivedKinds) != 1 || receivedKinds[0] != "class" {
		t.Errorf("expected kinds=[\"class\"] in request body, got %v", receivedKinds)
	}
}

func TestRunSymbols_APIError(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/symbols"):
			apiError(w, 500, "internal error")
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := symbolsProject
	defer func() { symbolsProject = old }()
	symbolsProject = proj
	symbolsLimit = 20
	symbolsKinds = nil

	_, err := captureOutput(func() error {
		return runSymbols(nil, []string{"Foo"})
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "search failed") {
		t.Errorf("expected 'search failed' in error, got: %v", err)
	}
}
