package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

func parseServers(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, b)
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		t.Fatalf("no mcpServers in result:\n%s", b)
	}
	return servers
}

func TestMcpServersJSONMerge_Empty(t *testing.T) {
	out, err := mcpServersJSONMerge(nil, "cix", "/usr/local/bin/cix", []string{"mcp"})
	if err != nil {
		t.Fatal(err)
	}
	servers := parseServers(t, out)
	entry, ok := servers["cix"].(map[string]any)
	if !ok {
		t.Fatalf("no cix entry: %v", servers)
	}
	if entry["command"] != "/usr/local/bin/cix" {
		t.Errorf("command = %v", entry["command"])
	}
	args, _ := entry["args"].([]any)
	if len(args) != 1 || args[0] != "mcp" {
		t.Errorf("args = %v, want [mcp]", entry["args"])
	}
}

func TestMcpServersJSONMerge_PreservesOthers(t *testing.T) {
	existing := []byte(`{
	  "mcpServers": { "other": { "command": "/bin/other" } },
	  "someTopLevel": true
	}`)
	out, err := mcpServersJSONMerge(existing, "cix", "/abs/cix", []string{"mcp"})
	if err != nil {
		t.Fatal(err)
	}
	servers := parseServers(t, out)
	if _, ok := servers["other"]; !ok {
		t.Error("existing 'other' server was dropped")
	}
	if _, ok := servers["cix"]; !ok {
		t.Error("cix server not added")
	}
	if !strings.Contains(string(out), "someTopLevel") {
		t.Error("unrelated top-level key was dropped")
	}
}

func TestMcpServersJSONMerge_Idempotent(t *testing.T) {
	first, err := mcpServersJSONMerge(nil, "cix", "/abs/cix", []string{"mcp"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mcpServersJSONMerge(first, "cix", "/abs/cix", []string{"mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("re-running changed the config:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestMcpServersJSONMerge_InvalidJSON(t *testing.T) {
	if _, err := mcpServersJSONMerge([]byte("{ not json"), "cix", "/abs/cix", []string{"mcp"}); err == nil {
		t.Error("want error for invalid existing JSON, got nil")
	}
}

func TestMcpServersJSONOtherNames(t *testing.T) {
	existing := []byte(`{"mcpServers":{"cix":{"command":"x"},"github":{"command":"y"},"docker":{"command":"z"}}}`)
	got := mcpServersJSONOtherNames(existing, "cix")
	want := "docker, github" // sorted, excluding cix
	if strings.Join(got, ", ") != want {
		t.Errorf("otherServerNames = %v, want %s", got, want)
	}
}

func TestMcpServersJSONRemove(t *testing.T) {
	existing, _ := mcpServersJSONMerge(
		[]byte(`{"mcpServers":{"other":{"command":"/bin/other"}}}`),
		"cix", "/abs/cix", []string{"mcp"})

	out, removed, err := mcpServersJSONRemove(existing, "cix")
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("expected removed=true")
	}
	servers := parseServers(t, out)
	if _, ok := servers["cix"]; ok {
		t.Error("cix not removed")
	}
	if _, ok := servers["other"]; !ok {
		t.Error("unrelated server 'other' was dropped")
	}

	// Removing again is a no-op.
	_, removed2, err := mcpServersJSONRemove(out, "cix")
	if err != nil {
		t.Fatal(err)
	}
	if removed2 {
		t.Error("second disconnect should report removed=false")
	}
}
