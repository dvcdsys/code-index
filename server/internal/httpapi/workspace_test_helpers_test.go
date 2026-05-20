package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/gitrepos"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/secrets"
	"github.com/dvcdsys/code-index/server/internal/workspaceprojects"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// reposRouter spins up a router with the full workspaces + git_repos +
// workspace_projects surface wired against an in-memory DB. Auth is
// disabled — the focus is the persistence + enqueue paths.
//
// The jobs worker pool is created but NOT started: tests only assert
// jobs landed in the right state. End-to-end clone+index runs against
// real git remotes — out of scope for unit tests.
// reposRouterDB is the explicit form returning the DB handle too.
// Tests that need to manipulate projects.status (a state normally owned
// by the indexer) call this variant.
func reposRouterDB(t *testing.T) (http.Handler, *jobs.Service, *sql.DB) {
	t.Helper()
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Setenv("CIX_SECRET_KEY", "")
	t.Setenv("CIX_SECRET_KEYFILE", "")
	sec, err := secrets.Open(secrets.OpenOptions{DataDir: t.TempDir(), AllowGenerate: true})
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}
	wsSvc := workspaces.New(d)
	ghSvc := githubtokens.New(d, sec)
	grSvc := gitrepos.New(d)
	wpSvc := workspaceprojects.New(d)
	jobsSvc := jobs.New(d, jobs.Options{Concurrency: 1, PollEvery: time.Hour})

	router := NewRouter(Deps{
		DB:                d,
		ServerVersion:     "test",
		APIVersion:        "v1",
		Backend:           "go",
		AuthDisabled:      true,
		Users:             seedlessUsers(d),
		Sessions:          seedlessSessions(d),
		APIKeys:           seedlessAPIKeys(d),
		Workspaces:        wsSvc,
		GithubTokens:      ghSvc,
		GitRepos:          grSvc,
		WorkspaceProjects: wpSvc,
		Jobs:              jobsSvc,
		PublicBaseURL:     "https://cix.example.test",
	})
	return router, jobsSvc, d
}

func reposRouter(t *testing.T) (http.Handler, *jobs.Service) {
	t.Helper()
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Setenv("CIX_SECRET_KEY", "")
	t.Setenv("CIX_SECRET_KEYFILE", "")
	sec, err := secrets.Open(secrets.OpenOptions{DataDir: t.TempDir(), AllowGenerate: true})
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}
	wsSvc := workspaces.New(d)
	ghSvc := githubtokens.New(d, sec)
	grSvc := gitrepos.New(d)
	wpSvc := workspaceprojects.New(d)
	jobsSvc := jobs.New(d, jobs.Options{Concurrency: 1, PollEvery: time.Hour})

	router := NewRouter(Deps{
		DB:                d,
		ServerVersion:     "test",
		APIVersion:        "v1",
		Backend:           "go",
		AuthDisabled:      true,
		Users:             seedlessUsers(d),
		Sessions:          seedlessSessions(d),
		APIKeys:           seedlessAPIKeys(d),
		Workspaces:        wsSvc,
		GithubTokens:      ghSvc,
		GitRepos:          grSvc,
		WorkspaceProjects: wpSvc,
		Jobs:              jobsSvc,
		PublicBaseURL:     "https://cix.example.test",
	})
	return router, jobsSvc
}

// createWS calls POST /api/v1/workspaces with the given name and returns
// the new workspace id.
func createWS(t *testing.T, router http.Handler, name string) string {
	t.Helper()
	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces", map[string]any{
		"name": name,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create workspace: %d (%s)", rr.Code, rr.Body.String())
	}
	var got workspacePayload
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	return got.ID
}
