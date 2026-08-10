package release

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.2.3", "1.2.4", -1},
		{"1.10.0", "1.9.0", 1}, // numeric, not lexicographic
		{"2.0.0", "1.99.99", 1},
		{"1.2", "1.2.0", 0}, // missing components are zero
		{"1.2.1", "1.2", 1},
	}
	for _, tc := range tests {
		if got := CompareSemver(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	if !IsNewer("0.3.0", "0.3.1") {
		t.Error("0.3.1 should be newer than 0.3.0")
	}
	if IsNewer("0.3.1", "0.3.1") {
		t.Error("a version is not newer than itself")
	}
	if IsNewer("0.4.0", "0.3.9") {
		t.Error("an older release should not be offered")
	}
	if IsNewer("0.3.0", "") {
		t.Error("an empty latest is not an update")
	}
	// Deliberately unlike the server's version-check, which treats an
	// unparseable current version as "anything is newer". That is right for a
	// banner and wrong for something that replaces the running application: a
	// development build is usually newer than the last release, so offering to
	// overwrite it would be a downgrade wearing an update's clothes.
	for _, current := range []string{"", "dev", "0.3.0-dev", "0.12.4-129-g8abef0b"} {
		if IsNewer(current, "9.9.9") {
			t.Errorf("IsNewer(%q, \"9.9.9\") = true; an unidentifiable build must not be replaced", current)
		}
	}
	// A "v" prefix on the installed version is tolerated.
	if !IsNewer("v0.3.0", "0.3.1") {
		t.Error("a leading v on the current version should be ignored")
	}
}

const releasesJSON = `[
  {"tag_name":"mac/v0.3.0","html_url":"https://example.test/0.3.0","draft":false,"prerelease":false,
   "assets":[{"name":"cix-0.3.0-arm64.dmg","browser_download_url":"https://example.test/a.dmg","size":123},
             {"name":"checksums.txt","browser_download_url":"https://example.test/c.txt","size":45}]},
  {"tag_name":"mac/v0.4.0","html_url":"https://example.test/0.4.0","draft":true,"prerelease":false,"assets":[]},
  {"tag_name":"mac/v0.3.5","html_url":"https://example.test/0.3.5","draft":false,"prerelease":true,"assets":[]},
  {"tag_name":"mac/v0.3.2-rc1","html_url":"https://example.test/rc","draft":false,"prerelease":false,"assets":[]},
  {"tag_name":"server/v9.9.9","html_url":"https://example.test/server","draft":false,"prerelease":false,"assets":[]},
  {"tag_name":"mac/v0.3.1","html_url":"https://example.test/0.3.1","draft":false,"prerelease":false,
   "assets":[{"name":"cix-0.3.1-arm64.dmg","browser_download_url":"https://example.test/b.dmg","size":999}]}
]`

func TestLatestFiltersTheStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `W/"abc123"`)
		w.Write([]byte(releasesJSON))
	}))
	defer srv.Close()

	c := New(DefaultRepo, "mac/v")
	c.BaseURL = srv.URL

	got, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	// 0.4.0 is a draft, 0.3.5 a prerelease, 0.3.2-rc1 carries a prerelease
	// suffix GitHub did not flag, and server/v9.9.9 is a different stream
	// entirely — sharing one repository with three tag streams is exactly why
	// the prefix filter exists.
	if got.Version != "0.3.1" {
		t.Errorf("Version = %q, want 0.3.1", got.Version)
	}
	if got.TagName != "mac/v0.3.1" {
		t.Errorf("TagName = %q, want mac/v0.3.1", got.TagName)
	}
	if c.ETag != `W/"abc123"` {
		t.Errorf("ETag = %q, want it captured for the next request", c.ETag)
	}
	if _, ok := got.AssetBySuffix(".dmg"); !ok {
		t.Error("the DMG asset should be findable by suffix")
	}
}

func TestLatestSendsAndHonoursETag(t *testing.T) {
	var sawIfNoneMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawIfNoneMatch = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	c := New(DefaultRepo, "mac/v")
	c.BaseURL = srv.URL
	c.ETag = `W/"cached"`

	_, err := c.Latest(context.Background())
	if !errors.Is(err, ErrNotModified) {
		t.Errorf("Latest() = %v, want ErrNotModified", err)
	}
	// The whole point: a 304 does not count against the 60-per-hour
	// unauthenticated limit, so checking often is free on quiet days.
	if sawIfNoneMatch != `W/"cached"` {
		t.Errorf("If-None-Match = %q, want the cached ETag", sawIfNoneMatch)
	}
	if c.ETag != `W/"cached"` {
		t.Errorf("ETag = %q; a 304 must not clear it", c.ETag)
	}
}

func TestLatestOnEmptyStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[{"tag_name":"server/v1.0.0","draft":false,"prerelease":false,"assets":[]}]`))
	}))
	defer srv.Close()

	c := New(DefaultRepo, "mac/v")
	c.BaseURL = srv.URL

	// A stream with no releases yet is a normal state for a new tag stream, not
	// an error — the app should stay quiet, not report a failure.
	got, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest on an empty stream returned an error: %v", err)
	}
	if got.Version != "" {
		t.Errorf("Version = %q, want empty", got.Version)
	}
}

func TestLatestRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := New(DefaultRepo, "mac/v")
	c.BaseURL = srv.URL

	if _, err := c.Latest(context.Background()); err == nil {
		t.Fatal("a 403 should be reported as an error")
	}
}
