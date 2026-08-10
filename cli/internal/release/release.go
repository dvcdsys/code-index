package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultRepo is the GitHub repository releases are published to.
const DefaultRepo = "dvcdsys/code-index"

// Release is one published release of the requested tag stream.
type Release struct {
	// Version is the tag with its stream prefix stripped: "0.3.1", not
	// "mac/v0.3.1".
	Version string
	TagName string
	HTMLURL string
	Assets  []Asset
}

// Asset is a downloadable file attached to a release.
type Asset struct {
	Name string
	URL  string
	Size int64
}

// AssetByName returns the named asset. The DMG and checksums.txt are located by
// name rather than by position, because the release workflow's upload order is
// not a contract.
func (r Release) AssetByName(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// AssetBySuffix returns the first asset whose name ends in suffix — used for
// the DMG, whose filename carries the version.
func (r Release) AssetBySuffix(suffix string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.HasSuffix(a.Name, suffix) {
			return a, true
		}
	}
	return Asset{}, false
}

// Client queries the GitHub releases API.
//
// Unauthenticated, which caps it at 60 requests per hour per IP — shared with
// anything else on the machine doing the same. That is why every response's
// ETag is kept and replayed: a 304 does not count against the limit, so an app
// that checks on every menu open costs nothing on the days there is no release.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	Repo    string

	// TagPrefix selects the stream, e.g. "mac/v".
	TagPrefix string

	// ETag is sent as If-None-Match and updated from each 200 response. Callers
	// persist it between runs.
	ETag string
}

func New(repo, tagPrefix string) *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		BaseURL:   "https://api.github.com",
		Repo:      repo,
		TagPrefix: tagPrefix,
	}
}

// ErrNotModified is returned when GitHub answers 304, meaning the cached result
// is still current.
var ErrNotModified = fmt.Errorf("not modified")

// Latest returns the highest-versioned published release of the stream.
//
// Returns a zero Release and no error when the stream has no releases yet —
// that is a normal state for a new tag stream, not a failure.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=30", c.BaseURL, c.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.ETag != "" {
		req.Header.Set("If-None-Match", c.ETag)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return Release{}, ErrNotModified
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		// Distinguished from a generic failure because it is self-healing and
		// the caller should stay quiet about it rather than alarm the user.
		return Release{}, fmt.Errorf("github rate limit reached (resets hourly)")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Release{}, fmt.Errorf("github returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var raw []struct {
		TagName    string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw); err != nil {
		return Release{}, fmt.Errorf("decode releases: %w", err)
	}
	c.ETag = resp.Header.Get("ETag")

	var best Release
	for _, r := range raw {
		if r.Draft || r.Prerelease {
			continue
		}
		version, ok := strings.CutPrefix(r.TagName, c.TagPrefix)
		if !ok {
			continue
		}
		// Belt and braces: drop anything still shaped like a prerelease or a
		// build-metadata tag even when GitHub did not flag it.
		if strings.ContainsAny(version, "-+") {
			continue
		}
		if best.Version != "" && CompareSemver(version, best.Version) <= 0 {
			continue
		}
		rel := Release{Version: version, TagName: r.TagName, HTMLURL: r.HTMLURL}
		for _, a := range r.Assets {
			rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size})
		}
		best = rel
	}
	return best, nil
}
