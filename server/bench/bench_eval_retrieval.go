//go:build bench_eval_retrieval

// bench_eval_retrieval runs queries.json against a live cix-server and
// reports precision@K plus anti-path leakage per category. Use it to
// compare retrieval quality before/after toggling CIX_EMBED_INCLUDE_PATH.
//
// Prerequisites:
//
//   1. cix-server is running locally (any port — pass via -url).
//   2. The target project has been indexed under whichever embedding format
//      you want to measure. To compare formats, run twice: once with the
//      old format (CIX_EMBED_INCLUDE_PATH=false + reindex), once with the
//      new format (CIX_EMBED_INCLUDE_PATH=true + reindex), capturing each
//      run's output to a separate file, then diff.
//
// Usage:
//
//   go run -tags=bench_eval_retrieval ./bench_eval_retrieval.go \
//       -url http://localhost:21847 \
//       -api-key "$(grep CIX_API_KEY ../.env | cut -d= -f2)" \
//       -project /Users/dvcdsys/Cursor/claude-code-index \
//       -queries queries.json
//
// Output: a per-category summary plus a per-query verdict line. Exit code 0
// always — this is a measurement tool, not a pass/fail gate.

package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type queryDef struct {
	Query         string   `json:"query"`
	Category      string   `json:"category"`
	K             int      `json:"k"`
	ExpectedPaths []string `json:"expected_paths"`
	AntiPaths     []string `json:"anti_paths"`
}

type queryFile struct {
	Queries []queryDef `json:"queries"`
}

type searchRequest struct {
	Query    string `json:"query"`
	Limit    int    `json:"limit"`
	MinScore *float64 `json:"min_score,omitempty"`
}

type searchResultItem struct {
	FilePath  string  `json:"file_path"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
	Language  string  `json:"language"`
}

type searchResponse struct {
	Results []searchResultItem `json:"results"`
	Total   int                `json:"total"`
}

func main() {
	urlFlag := flag.String("url", "http://localhost:21847", "cix-server URL")
	apiKey := flag.String("api-key", os.Getenv("CIX_API_KEY"), "API key (or set CIX_API_KEY env var)")
	project := flag.String("project", "", "absolute path of the indexed project (required)")
	queriesPath := flag.String("queries", "queries.json", "path to queries.json")
	verbose := flag.Bool("v", false, "print per-query verdicts in addition to per-category summary")
	flag.Parse()

	if *project == "" {
		fmt.Fprintln(os.Stderr, "error: -project is required")
		os.Exit(2)
	}
	if *apiKey == "" {
		fmt.Fprintln(os.Stderr, "warn: -api-key not set; requests will be unauthenticated (only works in dev mode)")
	}

	abs, err := filepath.Abs(*project)
	if err != nil {
		fmt.Fprintln(os.Stderr, "abs project path:", err)
		os.Exit(1)
	}

	data, err := os.ReadFile(*queriesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read queries:", err)
		os.Exit(1)
	}
	var qf queryFile
	if err := json.Unmarshal(data, &qf); err != nil {
		fmt.Fprintln(os.Stderr, "parse queries:", err)
		os.Exit(1)
	}

	hash := projectHash(abs)
	endpoint, err := url.JoinPath(*urlFlag, "api", "v1", "projects", hash, "search")
	if err != nil {
		fmt.Fprintln(os.Stderr, "join url:", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: 30 * time.Second}

	type catStats struct {
		queries          int
		precisionHits    int
		antiLeaks        int
		expectedTopCount int // sum of topK slots that were expected hits
		expectedTopBudget int // sum of K across queries (denominator for avg precision)
	}
	stats := map[string]*catStats{}

	if *verbose {
		fmt.Printf("%-12s %-8s %s\n", "category", "verdict", "query")
		fmt.Println(strings.Repeat("-", 80))
	}

	for _, q := range qf.Queries {
		cs, ok := stats[q.Category]
		if !ok {
			cs = &catStats{}
			stats[q.Category] = cs
		}
		cs.queries++

		k := q.K
		if k <= 0 {
			k = 5
		}
		cs.expectedTopBudget += k

		results, err := doSearch(client, endpoint, *apiKey, q.Query, k)
		if err != nil {
			fmt.Fprintln(os.Stderr, "query failed:", q.Query, err)
			continue
		}

		hit := false
		antiHit := false
		expectedHits := 0
		for _, r := range results {
			rel, _ := filepath.Rel(abs, r.FilePath)
			if rel == "" {
				rel = r.FilePath
			}
			if matchesAny(rel, q.ExpectedPaths) {
				hit = true
				expectedHits++
			}
			if matchesAny(rel, q.AntiPaths) {
				antiHit = true
			}
		}
		if hit {
			cs.precisionHits++
		}
		if antiHit {
			cs.antiLeaks++
		}
		cs.expectedTopCount += expectedHits

		if *verbose {
			verdict := "PASS"
			if !hit {
				verdict = "MISS"
			}
			extra := ""
			if antiHit {
				extra = "  ⚠ anti-leak"
			}
			fmt.Printf("%-12s %-8s %s%s\n", q.Category, verdict, q.Query, extra)
		}
	}

	// Summary
	fmt.Println()
	fmt.Println("=== Retrieval quality summary ===")
	fmt.Printf("%-12s %8s %12s %12s %14s\n", "category", "queries", "any-hit @K", "anti-leak", "avg recall@K")
	fmt.Println(strings.Repeat("-", 72))
	cats := make([]string, 0, len(stats))
	for c := range stats {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	for _, c := range cats {
		s := stats[c]
		anyHitPct := pct(s.precisionHits, s.queries)
		antiLeakPct := pct(s.antiLeaks, s.queries)
		recallPct := pct(s.expectedTopCount, s.expectedTopBudget)
		fmt.Printf("%-12s %8d %11.1f%% %11.1f%% %13.1f%%\n",
			c, s.queries, anyHitPct, antiLeakPct, recallPct)
	}
	fmt.Println()
	fmt.Println("Note: any-hit@K = fraction of queries with at least one expected_paths match in top-K.")
	fmt.Println("      anti-leak  = fraction of queries with any anti_paths match in top-K (LOWER is better).")
	fmt.Println("      avg recall@K = sum(expected hits) / sum(K) — fraction of top-K slots that were expected.")
}

func doSearch(client *http.Client, endpoint, apiKey, query string, limit int) ([]searchResultItem, error) {
	body, _ := json.Marshal(searchRequest{Query: query, Limit: limit})
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(snippet))
	}
	var sr searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	return sr.Results, nil
}

func matchesAny(rel string, prefixes []string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range prefixes {
		p = filepath.ToSlash(p)
		// Treat trailing slash as "any descendant" match; otherwise require
		// either an exact match or a directory match.
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(rel, p) {
				return true
			}
		} else {
			if rel == p || strings.HasPrefix(rel, p+"/") {
				return true
			}
		}
	}
	return false
}

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return 100 * float64(n) / float64(d)
}

// projectHash mirrors projects.HashPath: first 16 hex chars of sha1(path).
func projectHash(absPath string) string {
	h := sha1.New()
	h.Write([]byte(absPath))
	b := h.Sum(nil)
	const hexchars = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 0; i < 8; i++ {
		out[i*2] = hexchars[b[i]>>4]
		out[i*2+1] = hexchars[b[i]&0xf]
	}
	return string(out)
}
