package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	// Clear any CIX_* that may leak in from the shell. We register t.Setenv
	// first for each key so the test-scoped cleanup restores pre-test values,
	// then force-Unsetenv so Load() sees no var and picks its default.
	unsetAll(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Port != 21847 {
		t.Errorf("Port default = %d, want 21847", c.Port)
	}
	if c.EmbeddingModel != "awhiteside/CodeRankEmbed-Q8_0-GGUF" {
		t.Errorf("EmbeddingModel default = %q", c.EmbeddingModel)
	}
	if c.MaxChunkTokens != 1500 {
		t.Errorf("MaxChunkTokens default = %d", c.MaxChunkTokens)
	}
	if c.MaxFileSize != 524288 {
		t.Errorf("MaxFileSize default = %d", c.MaxFileSize)
	}
	if len(c.ExcludedDirs) == 0 || c.ExcludedDirs[0] != "node_modules" {
		t.Errorf("ExcludedDirs default unexpected: %v", c.ExcludedDirs)
	}
}

func TestReposDirPrecedence(t *testing.T) {
	const sqlite = "/srv/cix/sqlite/projects.db"
	defaultDir := "/srv/cix/sqlite/repos"

	t.Run("default", func(t *testing.T) {
		unsetAll(t)
		t.Setenv("CIX_SQLITE_PATH", sqlite)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.WorkspacesDataDir != defaultDir {
			t.Errorf("default = %q, want %q", c.WorkspacesDataDir, defaultDir)
		}
	})

	t.Run("legacy alias", func(t *testing.T) {
		unsetAll(t)
		t.Setenv("CIX_SQLITE_PATH", sqlite)
		t.Setenv("CIX_WORKSPACES_DATA_DIR", "/legacy/repos")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.WorkspacesDataDir != "/legacy/repos" {
			t.Errorf("legacy alias = %q, want /legacy/repos", c.WorkspacesDataDir)
		}
	})

	t.Run("CIX_REPOS_DIR wins over legacy", func(t *testing.T) {
		unsetAll(t)
		t.Setenv("CIX_SQLITE_PATH", sqlite)
		t.Setenv("CIX_WORKSPACES_DATA_DIR", "/legacy/repos")
		t.Setenv("CIX_REPOS_DIR", "/mnt/big-volume/repos")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.WorkspacesDataDir != "/mnt/big-volume/repos" {
			t.Errorf("CIX_REPOS_DIR = %q, want /mnt/big-volume/repos", c.WorkspacesDataDir)
		}
	})
}

func TestLoadOverrides(t *testing.T) {
	unsetAll(t)
	// The unsetAll above wipes env before Setenv registers restore callbacks.
	// Subsequent t.Setenv calls both set the value for this test and register
	// proper cleanups.
	t.Setenv("CIX_PORT", "9002")
	t.Setenv("CIX_API_KEY", "secret")
	t.Setenv("CIX_EMBEDDING_MODEL", "test/Model-Name")
	t.Setenv("CIX_SQLITE_PATH", "/tmp/test.db")
	t.Setenv("CIX_EXCLUDED_DIRS", "a, b ,c")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Port != 9002 {
		t.Errorf("Port = %d, want 9002", c.Port)
	}
	if c.APIKey != "secret" {
		t.Errorf("APIKey = %q", c.APIKey)
	}
	if got, want := len(c.ExcludedDirs), 3; got != want {
		t.Fatalf("ExcludedDirs len = %d, want %d (%v)", got, want, c.ExcludedDirs)
	}
	if c.ExcludedDirs[1] != "b" {
		t.Errorf("ExcludedDirs[1] = %q, want 'b'", c.ExcludedDirs[1])
	}

	if got := c.ModelSafeName(); got != "test_model_name" {
		t.Errorf("ModelSafeName = %q", got)
	}
	// LegacyDynamicSQLitePath still reconstructs the OLD per-model filename
	// (used only by the boot-time adoption migration).
	if got := c.LegacyDynamicSQLitePath(); got != "/tmp/test_test_model_name.db" {
		t.Errorf("LegacyDynamicSQLitePath = %q", got)
	}
	// ChromaDirFor joins the identity path components under the chroma base.
	comps := []string{"voyage", "voyage_code_3", "2048", "float"}
	if got, want := c.ChromaDirFor(comps), filepath.Join(append([]string{c.ChromaPersistDir}, comps...)...); got != want {
		t.Errorf("ChromaDirFor = %q, want %q", got, want)
	}
	// VectorDirFor mirrors it under the vectors container — same components,
	// same nesting — which is what pairs a live database with the legacy
	// chromem directory it was imported from.
	if got, want := c.VectorDirFor(comps), filepath.Join(append([]string{c.VectorsDir}, comps...)...); got != want {
		t.Errorf("VectorDirFor = %q, want %q", got, want)
	}
}

// The vectors container defaults to a SIBLING of the chroma container. A
// deployment that only overrides CIX_CHROMA_PERSIST_DIR (every container does)
// must still land its vector databases on the same persistent volume.
func TestVectorsDirDefaultsBesideChroma(t *testing.T) {
	unsetAll(t)
	t.Setenv("CIX_CHROMA_PERSIST_DIR", "/data/chroma")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := c.VectorsDir, "/data/vectors"; got != want {
		t.Errorf("VectorsDir = %q, want %q", got, want)
	}
	if c.VectorMMapSize != 0 {
		t.Errorf("VectorMMapSize = %d, want 0 (off by default)", c.VectorMMapSize)
	}

	t.Setenv("CIX_VECTORS_DIR", "/elsewhere/vec")
	t.Setenv("CIX_VECTOR_MMAP_SIZE", "2147483648")
	c, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := c.VectorsDir, "/elsewhere/vec"; got != want {
		t.Errorf("VectorsDir = %q, want %q", got, want)
	}
	if got, want := c.VectorMMapSize, int64(2147483648); got != want {
		t.Errorf("VectorMMapSize = %d, want %d", got, want)
	}
}

func TestLoadPhase3Defaults(t *testing.T) {
	unsetAll(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LlamaTransport != "unix" {
		t.Errorf("LlamaTransport default = %q, want unix", c.LlamaTransport)
	}
	if c.LlamaCtxSize != 2048 {
		t.Errorf("LlamaCtxSize default = %d, want 2048", c.LlamaCtxSize)
	}
	if c.LlamaStartupSec != 60 {
		t.Errorf("LlamaStartupSec default = %d, want 60", c.LlamaStartupSec)
	}
	if !c.EmbeddingsEnabled {
		t.Errorf("EmbeddingsEnabled default = false, want true")
	}
	// GPU layers default depends on GOOS. On darwin we expect -1 (Metal all);
	// on any other platform 0. Either way the value must be set explicitly.
	if c.LlamaNGpuLayers != -1 && c.LlamaNGpuLayers != 0 {
		t.Errorf("LlamaNGpuLayers default = %d, expected -1 or 0", c.LlamaNGpuLayers)
	}
	if c.GGUFCacheDir == "" {
		t.Error("GGUFCacheDir default is empty")
	}
}

func TestValidateBadTransport(t *testing.T) {
	unsetAll(t)
	// Auth-off so the auth-gate check (which runs first) lets us reach the
	// transport check we actually want to exercise.
	t.Setenv("CIX_AUTH_DISABLED", "true")
	t.Setenv("CIX_LLAMA_TRANSPORT", "udp")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "CIX_LLAMA_TRANSPORT") {
		t.Fatalf("Validate: expected transport error, got %v", err)
	}
}

func TestValidateBadCtx(t *testing.T) {
	unsetAll(t)
	t.Setenv("CIX_AUTH_DISABLED", "true")
	t.Setenv("CIX_LLAMA_CTX", "0")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "CIX_LLAMA_CTX") {
		t.Fatalf("Validate: expected ctx error, got %v", err)
	}
}

// TestValidate_NoLongerGuardsAuth — the explicit-or-die check on
// CIX_API_KEY moved out of config.Validate when the dashboard branch
// introduced per-user accounts. Auth gating is now main.go's job (it
// refuses to start with an empty users table and no
// CIX_BOOTSTRAP_ADMIN_* env). This test pins down the new permissive
// behaviour so a future revert wouldn't sneak past CI.
func TestValidate_NoLongerGuardsAuth(t *testing.T) {
	cases := []struct {
		name    string
		apiKey  string
		authOff string
	}{
		{name: "no key, no flag", apiKey: "", authOff: ""},
		{name: "no key, flag=false", apiKey: "", authOff: "false"},
		{name: "no key, flag=true", apiKey: "", authOff: "true"},
		{name: "key set, no flag", apiKey: "secret"},
		{name: "key set, flag=true", apiKey: "secret", authOff: "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetAll(t)
			if tc.apiKey != "" {
				t.Setenv("CIX_API_KEY", tc.apiKey)
			}
			if tc.authOff != "" {
				t.Setenv("CIX_AUTH_DISABLED", tc.authOff)
			}
			c, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if err := c.Validate(); err != nil {
				t.Errorf("Validate must not block on auth fields, got %v", err)
			}
		})
	}
}

// TestLoadBootstrapFields ensures the new CIX_BOOTSTRAP_ADMIN_* env vars
// land on the Config. The actual seed-or-skip decision lives in main.go
// where it has access to the users service.
func TestLoadBootstrapFields(t *testing.T) {
	unsetAll(t)
	t.Setenv("CIX_BOOTSTRAP_ADMIN_EMAIL", "admin@example.com")
	t.Setenv("CIX_BOOTSTRAP_ADMIN_PASSWORD", "changeme")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.BootstrapAdminEmail != "admin@example.com" {
		t.Errorf("BootstrapAdminEmail = %q", c.BootstrapAdminEmail)
	}
	if c.BootstrapAdminPassword != "changeme" {
		t.Errorf("BootstrapAdminPassword not loaded")
	}
}

func TestLoadEmbeddingsEnabledToggle(t *testing.T) {
	unsetAll(t)
	t.Setenv("CIX_EMBEDDINGS_ENABLED", "false")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.EmbeddingsEnabled {
		t.Error("EmbeddingsEnabled should be false when env set to false")
	}
}

func TestLoadBadInt(t *testing.T) {
	unsetAll(t)
	t.Setenv("CIX_PORT", "not-a-number")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for bad CIX_PORT")
	}
}

// unsetAll wipes every CIX_* env var so Load() exercises its defaults.
// We first call t.Setenv to register a per-test restore hook, then
// os.Unsetenv so LookupEnv returns ok=false inside the test body.
func unsetAll(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CIX_API_KEY", "CIX_PORT", "CIX_EMBEDDING_MODEL",
		"CIX_CHROMA_PERSIST_DIR", "CIX_SQLITE_PATH", "CIX_MAX_FILE_SIZE",
		"CIX_EXCLUDED_DIRS", "CIX_MAX_EMBEDDING_CONCURRENCY",
		"CIX_EMBEDDING_QUEUE_TIMEOUT", "CIX_MAX_CHUNK_TOKENS",
		// Phase 3 additions — kept in the same helper so new tests cannot
		// accidentally inherit values from a developer shell.
		"CIX_GGUF_PATH", "CIX_GGUF_CACHE_DIR", "CIX_LLAMA_BIN_DIR",
		"CIX_LLAMA_SOCKET", "CIX_LLAMA_TRANSPORT", "CIX_LLAMA_CTX",
		"CIX_N_GPU_LAYERS", "CIX_LLAMA_STARTUP_TIMEOUT", "CIX_EMBEDDINGS_ENABLED",
		// Auth gating — without this, a developer's shell with
		// CIX_AUTH_DISABLED=true would silently make Validate succeed
		// on tests that expect a missing-key failure.
		"CIX_AUTH_DISABLED",
		// Bootstrap — wipe so the Load tests don't accidentally inherit
		// a developer's local bootstrap-admin shell vars.
		"CIX_BOOTSTRAP_ADMIN_EMAIL", "CIX_BOOTSTRAP_ADMIN_PASSWORD",
		// Repo clone dir + its legacy alias.
		"CIX_REPOS_DIR", "CIX_WORKSPACES_DATA_DIR",
		// Vector store container + its mmap knob.
		"CIX_VECTORS_DIR", "CIX_VECTOR_MMAP_SIZE",
	} {
		t.Setenv(k, "sentinel")
		osUnsetenv(k)
	}
}

func TestListenAddr(t *testing.T) {
	tests := []struct {
		name string
		bind string
		port int
		want string
	}{
		// The empty default must keep producing ":port". Every existing
		// deployment — every container in particular — depends on binding all
		// interfaces, so narrowing this is opt-in and never implicit.
		{"default is all interfaces", "", 21847, ":21847"},
		{"loopback", "127.0.0.1", 21847, "127.0.0.1:21847"},
		{"explicit all interfaces", "0.0.0.0", 8080, "0.0.0.0:8080"},
		{"hostname", "localhost", 21847, "localhost:21847"},
		// JoinHostPort brackets IPv6; a naive host+":"+port would produce
		// "::1:21847", which net.Listen reads as a different address entirely.
		{"ipv6 loopback is bracketed", "::1", 21847, "[::1]:21847"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{BindAddr: tc.bind, Port: tc.port}
			if got := c.ListenAddr(); got != tc.want {
				t.Errorf("ListenAddr() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLocalOnly(t *testing.T) {
	tests := map[string]bool{
		"":            false, // all interfaces
		"0.0.0.0":     false,
		"192.168.1.5": false,
		"127.0.0.1":   true,
		"127.0.0.2":   true, // the whole 127/8 block is loopback
		"::1":         true,
		"localhost":   true,
		"LOCALHOST":   true,
	}
	for bind, want := range tests {
		if got := (&Config{BindAddr: bind}).LocalOnly(); got != want {
			t.Errorf("LocalOnly() for %q = %v, want %v", bind, got, want)
		}
	}
}

func TestValidateBindAddr(t *testing.T) {
	valid := []string{"", "127.0.0.1", "0.0.0.0", "localhost", "::1", "192.168.1.5"}
	for _, addr := range valid {
		if err := validateBindAddr(addr); err != nil {
			t.Errorf("validateBindAddr(%q) = %v, want nil", addr, err)
		}
	}
	// The two mistakes worth catching: both bind as a hostname on some systems
	// and then fail to resolve, giving a server that is silently unreachable
	// instead of one that refused to start.
	invalid := []string{"http://127.0.0.1", "https://cix.local", "127.0.0.1:21847", "localhost:8080"}
	for _, addr := range invalid {
		if err := validateBindAddr(addr); err == nil {
			t.Errorf("validateBindAddr(%q) = nil, want an error", addr)
		}
	}
}
