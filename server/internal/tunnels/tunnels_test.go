package tunnels

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/githubapi"
	"github.com/dvcdsys/code-index/server/internal/gitrepos"
)

func TestQuickURLRegexExtractsTunnelURL(t *testing.T) {
	line := "2026-05-20T10:00:00Z INF +--------------------------------------------------------+ | Your quick Tunnel has been created! Visit it at: | | https://random-words-here.trycloudflare.com |"
	got := quickURLRe.FindString(line)
	want := "https://random-words-here.trycloudflare.com"
	if got != want {
		t.Fatalf("quick URL: got %q want %q", got, want)
	}
}

func TestManagerDisabledIsNoOp(t *testing.T) {
	m := NewManager(ManagerConfig{}, nil)
	// No Apply yet → nothing running.
	if m.URL() != "" {
		t.Fatalf("disabled URL should be empty, got %q", m.URL())
	}
	if st := m.Status(); st.State != StateDisabled {
		t.Fatalf("disabled state: got %q", st.State)
	}
	cat := m.Catalog()
	if len(cat) != 2 {
		t.Fatalf("catalog should list 2 providers, got %d", len(cat))
	}
	if cat[0].Provider != ProviderCloudflare || cat[1].Provider != ProviderNgrok {
		t.Fatalf("catalog should list cloudflare then ngrok, got %+v", cat)
	}
	if !cat[1].Available {
		t.Fatalf("ngrok should now be available, got %+v", cat[1])
	}
	if _, err := m.TestConnection(context.Background()); err != ErrNoActiveTunnel {
		t.Fatalf("expected ErrNoActiveTunnel, got %v", err)
	}
}

func TestManagerApplyDisabledIsNoOp(t *testing.T) {
	m := NewManager(ManagerConfig{}, nil)
	if err := m.Apply(context.Background(), Settings{Enabled: false}); err != nil {
		t.Fatalf("Apply disabled: %v", err)
	}
	if m.URL() != "" || m.Status().State != StateDisabled {
		t.Fatalf("disabled apply should leave no active tunnel")
	}
}

func TestManagerApplyRejectsNgrok(t *testing.T) {
	m := NewManager(ManagerConfig{}, nil)
	err := m.Apply(context.Background(), Settings{Enabled: true, Provider: ProviderNgrok})
	if err == nil {
		t.Fatal("expected ngrok to be rejected as not implemented")
	}
	if st := m.Status(); st.State != StateFailed {
		t.Fatalf("status after failed apply should be failed, got %q", st.State)
	}
}

func TestManagerApplyMissingBinaryFails(t *testing.T) {
	m := NewManager(ManagerConfig{
		Cloudflare: CloudflareInfra{BinPath: "definitely-not-a-real-binary-xyz", MetricsAddr: "127.0.0.1:0", StartupSec: 1},
	}, nil)
	err := m.Apply(context.Background(), Settings{Enabled: true, Provider: ProviderCloudflare, Mode: CloudflareModeQuick})
	if err == nil {
		t.Fatal("expected missing-binary apply to fail")
	}
	if st := m.Status(); st.State != StateFailed {
		t.Fatalf("status after failed apply should be failed, got %q", st.State)
	}
}

func TestManagerApplyNgrokMissingBinaryFails(t *testing.T) {
	m := NewManager(ManagerConfig{
		Ngrok: NgrokInfra{BinPath: "definitely-not-a-real-ngrok-xyz", StartupSec: 1},
	}, nil)
	err := m.Apply(context.Background(), Settings{
		Enabled: true, Provider: ProviderNgrok, Mode: ngrokModeQuick, Token: "tok",
	})
	if err == nil {
		t.Fatal("expected missing-binary apply to fail")
	}
	if st := m.Status(); st.State != StateFailed || st.Provider != ProviderNgrok {
		t.Fatalf("status after failed ngrok apply: %+v", st)
	}
}

func TestNgrokConstructionRequiresToken(t *testing.T) {
	_, err := newNgrokProvider(NgrokConfig{Mode: ngrokModeQuick, BinPath: "sh"}, 8080, nil)
	if err == nil || !contains(err.Error(), "authtoken") {
		t.Fatalf("expected authtoken-required error, got %v", err)
	}
}

func TestNgrokStartedURLRegex(t *testing.T) {
	line := `{"addr":"http://localhost:21847","lvl":"info","msg":"started tunnel","name":"command_line","obj":"tunnels","url":"https://abcd-1-2-3-4.ngrok-free.app"}`
	m := ngrokStartedRe.FindStringSubmatch(line)
	if len(m) != 2 || m[1] != "https://abcd-1-2-3-4.ngrok-free.app" {
		t.Fatalf("ngrok url parse: %v", m)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestSetURLFiresCallbackOnChange(t *testing.T) {
	p := &cloudflareProvider{cfg: CloudflareConfig{Mode: CloudflareModeQuick}, logger: slog.Default()}
	ch := make(chan string, 1)
	p.OnURLChange(func(u string) { ch <- u })

	p.setURL("https://abc.trycloudflare.com")
	select {
	case got := <-ch:
		if got != "https://abc.trycloudflare.com" {
			t.Fatalf("callback url: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("callback not fired on first URL")
	}

	// Same URL again → no callback.
	p.setURL("https://abc.trycloudflare.com")
	select {
	case got := <-ch:
		t.Fatalf("callback should not fire for unchanged URL, got %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCloudflareTestConnectionRoundTrips(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := &cloudflareProvider{
		cfg:        CloudflareConfig{Mode: CloudflareModeQuick},
		publicURL:  srv.URL,
		httpClient: &http.Client{Timeout: 2 * time.Second},
	}
	res, err := p.TestConnection(context.Background())
	if err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if !res.OK || res.StatusCode != http.StatusOK {
		t.Fatalf("expected OK round-trip, got %+v", res)
	}
}

func TestCloudflareTestConnectionNoURL(t *testing.T) {
	p := &cloudflareProvider{cfg: CloudflareConfig{Mode: CloudflareModeQuick}, httpClient: &http.Client{}}
	res, err := p.TestConnection(context.Background())
	if err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if res.OK {
		t.Fatal("expected not-OK when no URL")
	}
}

// --- reconciler fakes ---

type fakeRepos struct {
	repos  []gitrepos.GitRepo
	setIDs map[string]int64
}

func (f *fakeRepos) ListAll(_ context.Context) ([]gitrepos.GitRepo, error) { return f.repos, nil }
func (f *fakeRepos) SetWebhookID(_ context.Context, projectPath string, hookID int64) error {
	if f.setIDs == nil {
		f.setIDs = map[string]int64{}
	}
	f.setIDs[projectPath] = hookID
	return nil
}

type fakeTokens struct{}

func (fakeTokens) Reveal(_ context.Context, _ string) (string, error) { return "ghp_test", nil }
func (fakeTokens) Touch(_ context.Context, _ string) error            { return nil }

type fakeGH struct {
	created, updated   int
	failUpdateNotFound bool
}

func (f *fakeGH) CreateWebhook(_ context.Context, _ githubapi.CreateWebhookOptions) (githubapi.HookResponse, error) {
	f.created++
	return githubapi.HookResponse{ID: 999}, nil
}
func (f *fakeGH) UpdateWebhook(_ context.Context, opts githubapi.UpdateWebhookOptions) (githubapi.HookResponse, error) {
	if f.failUpdateNotFound {
		return githubapi.HookResponse{}, fmt.Errorf("%w: gone", githubapi.ErrNotFound)
	}
	f.updated++
	return githubapi.HookResponse{ID: opts.HookID}, nil
}

func i64(v int64) *int64 { return &v }

func TestReconcileUpdatesCreatesSkips(t *testing.T) {
	repos := &fakeRepos{repos: []gitrepos.GitRepo{
		{ProjectPath: "github.com/o/a@main", PathHash: "aaa", GitHubURL: "https://github.com/o/a", TokenID: "t1", WebhookSecret: "s", WebhookMode: gitrepos.WebhookModeAuto, WebhookID: i64(42)},
		{ProjectPath: "github.com/o/b@main", PathHash: "bbb", GitHubURL: "https://github.com/o/b", TokenID: "t1", WebhookSecret: "s", WebhookMode: gitrepos.WebhookModeAuto},
		{ProjectPath: "github.com/o/c@main", PathHash: "ccc", GitHubURL: "https://github.com/o/c", TokenID: "t1", WebhookSecret: "s", WebhookMode: gitrepos.WebhookModeManual},
		{ProjectPath: "github.com/o/d@main", PathHash: "ddd", GitHubURL: "https://github.com/o/d", TokenID: "", WebhookSecret: "s", WebhookMode: gitrepos.WebhookModeAuto},
	}}
	gh := &fakeGH{}
	r := NewReconciler(repos, fakeTokens{}, gh, nil)

	res, err := r.Reconcile(context.Background(), "https://x.trycloudflare.com")
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Total != 3 { // 3 auto repos (a, b, d); manual c excluded
		t.Fatalf("total auto repos: got %d want 3", res.Total)
	}
	if res.Updated != 1 || res.Created != 1 || res.Skipped != 1 {
		t.Fatalf("counts: updated=%d created=%d skipped=%d (%+v)", res.Updated, res.Created, res.Skipped, res.Outcomes)
	}
	if gh.updated != 1 || gh.created != 1 {
		t.Fatalf("gh calls: updated=%d created=%d", gh.updated, gh.created)
	}
	if repos.setIDs["github.com/o/b@main"] != 999 {
		t.Fatalf("created repo should persist new webhook id, got %v", repos.setIDs)
	}
}

func TestReconcileRecreatesWhenHookGone(t *testing.T) {
	repos := &fakeRepos{repos: []gitrepos.GitRepo{
		{ProjectPath: "github.com/o/a@main", PathHash: "aaa", GitHubURL: "https://github.com/o/a", TokenID: "t1", WebhookSecret: "s", WebhookMode: gitrepos.WebhookModeAuto, WebhookID: i64(42)},
	}}
	gh := &fakeGH{failUpdateNotFound: true}
	r := NewReconciler(repos, fakeTokens{}, gh, nil)

	res, err := r.Reconcile(context.Background(), "https://x.trycloudflare.com")
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Created != 1 || gh.created != 1 {
		t.Fatalf("expected recreate on 404 update, got created=%d (%+v)", res.Created, res.Outcomes)
	}
}

func TestReconcileNoBaseURLIsNoOp(t *testing.T) {
	repos := &fakeRepos{repos: []gitrepos.GitRepo{
		{ProjectPath: "github.com/o/a@main", PathHash: "aaa", GitHubURL: "https://github.com/o/a", TokenID: "t1", WebhookMode: gitrepos.WebhookModeAuto, WebhookID: i64(42)},
	}}
	gh := &fakeGH{}
	r := NewReconciler(repos, fakeTokens{}, gh, nil)
	res, err := r.Reconcile(context.Background(), "")
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Total != 0 || gh.updated != 0 || gh.created != 0 {
		t.Fatalf("empty base URL should be a no-op, got %+v", res)
	}
}
