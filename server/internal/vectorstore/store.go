// Package vectorstore wraps chromem-go to provide a persistent vector store
// with the same semantics as the Python VectorStoreService (api/app/services/vector_store.py).
//
// Collection naming and document ID schemes are kept identical to Python so
// that a future migration script can read the chromem-go data without mapping.
package vectorstore

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	chromem "github.com/philippgille/chromem-go"
)

const upsertBatchSize = 500

// Chunk is the input unit for UpsertChunks.
// Mirrors the metadata keys stored by the Python VectorStoreService.
type Chunk struct {
	Content    string
	FilePath   string
	StartLine  int
	EndLine    int
	ChunkType  string
	SymbolName string
	Language   string
}

// SearchResult mirrors the Python SearchResultItem schema returned by /search.
type SearchResult struct {
	FilePath   string
	StartLine  int
	EndLine    int
	Content    string
	Score      float32 // cosine similarity in [0,1], rounded to 4 decimal places
	ChunkType  string
	SymbolName string
	Language   string
}

// Store wraps a persistent chromem-go DB.
type Store struct {
	db *chromem.DB
}

// Open returns a Store backed by a persistent chromem-go DB at path.
// The directory is created by chromem-go if it does not exist.
func Open(path string) (*Store, error) {
	db, err := chromem.NewPersistentDB(path, false)
	if err != nil {
		return nil, fmt.Errorf("vectorstore open %q: %w", path, err)
	}
	return &Store{db: db}, nil
}

// collectionName mirrors Python: f"project_{md5hex(project_path)}"
func collectionName(projectPath string) string {
	h := md5.Sum([]byte(projectPath))
	return fmt.Sprintf("project_%x", h)
}

// CollectionName is the exported alias for the per-project chromem-go
// collection identifier. The dashboard's project-detail card uses it to
// resolve the on-disk directory under cfg.DynamicChromaPersistDir().
func CollectionName(projectPath string) string { return collectionName(projectPath) }

// docID format: "{md5hex(filePath)[:12]}:{startLine}-{endLine}:{idx}"
//
// The positional `idx` is required because overlapping-window or repeated
// chunkers can emit two chunks with identical (filePath, startLine, endLine);
// without idx the second silently overwrites the first in chromem-go.
//
// `h[:6]` gives 12 hex characters. Format is frozen — existing prod indexes
// (including those imported from the prior Python backend) reference these
// ids on disk; changing the shape requires a full reindex.
func docID(filePath string, startLine, endLine, idx int) string {
	h := md5.Sum([]byte(filePath))
	return fmt.Sprintf("%x:%d-%d:%d", h[:6], startLine, endLine, idx)
}

// embedNotUsed is a stub embedding func. chromem-go requires one, but we always
// supply pre-computed embeddings via Document.Embedding, so this is never called.
func embedNotUsed(_ context.Context, _ string) ([]float32, error) {
	return nil, errors.New("vectorstore: embed func must not be called when embeddings are pre-computed")
}

func (s *Store) getOrCreateCollection(projectPath string) (*chromem.Collection, error) {
	return s.db.GetOrCreateCollection(
		collectionName(projectPath),
		map[string]string{"hnsw:space": "cosine"},
		embedNotUsed,
	)
}

// UpsertChunks stores or overwrites chunks with their pre-computed embeddings.
// chunks and embeddings must be the same length.
// Mirrors Python VectorStoreService.upsert_chunks.
func (s *Store) UpsertChunks(ctx context.Context, projectPath string, chunks []Chunk, embeddings [][]float32) error {
	if len(chunks) != len(embeddings) {
		return fmt.Errorf("vectorstore: chunks(%d) and embeddings(%d) length mismatch", len(chunks), len(embeddings))
	}
	col, err := s.getOrCreateCollection(projectPath)
	if err != nil {
		return err
	}

	docs := make([]chromem.Document, len(chunks))
	for i, c := range chunks {
		docs[i] = chromem.Document{
			ID:      docID(c.FilePath, c.StartLine, c.EndLine, i),
			Content: c.Content,
			Metadata: map[string]string{
				"file_path":   c.FilePath,
				"start_line":  strconv.Itoa(c.StartLine),
				"end_line":    strconv.Itoa(c.EndLine),
				"chunk_type":  c.ChunkType,
				"symbol_name": c.SymbolName,
				"language":    c.Language,
			},
			Embedding: embeddings[i],
		}
	}

	for i := 0; i < len(docs); i += upsertBatchSize {
		end := i + upsertBatchSize
			end = min(end, len(docs))
		if err := col.AddDocuments(ctx, docs[i:end], 1); err != nil {
			return fmt.Errorf("vectorstore upsert batch [%d:%d]: %w", i, end, err)
		}
	}
	return nil
}

// Search performs a nearest-neighbor search using a pre-computed query embedding.
// where is an optional metadata filter (e.g. {"language": "go"}).
// Mirrors Python VectorStoreService.search.
func (s *Store) Search(ctx context.Context, projectPath string, queryEmbedding []float32, limit int, where map[string]string) ([]SearchResult, error) {
	col, err := s.getOrCreateCollection(projectPath)
	if err != nil {
		return nil, err
	}
	count := col.Count()
	if count == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	limit = min(limit, count)
	results, err := col.QueryEmbedding(ctx, queryEmbedding, limit, where, nil)
	if err != nil {
		return nil, fmt.Errorf("vectorstore search: %w", err)
	}

	out := make([]SearchResult, len(results))
	for i, r := range results {
		startLine, _ := strconv.Atoi(r.Metadata["start_line"])
		endLine, _ := strconv.Atoi(r.Metadata["end_line"])
		out[i] = SearchResult{
			FilePath:   r.Metadata["file_path"],
			StartLine:  startLine,
			EndLine:    endLine,
			Content:    r.Content,
			Score:      round4(r.Similarity),
			ChunkType:  r.Metadata["chunk_type"],
			SymbolName: r.Metadata["symbol_name"],
			Language:   r.Metadata["language"],
		}
	}
	return out, nil
}

// DeleteByFile removes all chunks for a given file within a project.
// Mirrors Python VectorStoreService.delete_by_file.
func (s *Store) DeleteByFile(ctx context.Context, projectPath, filePath string) error {
	col, err := s.getOrCreateCollection(projectPath)
	if err != nil {
		return err
	}
	if err := col.Delete(ctx, map[string]string{"file_path": filePath}, nil); err != nil {
		return fmt.Errorf("vectorstore delete by file %q: %w", filePath, err)
	}
	return nil
}

// DeleteCollection removes the entire vector collection for a project.
// Mirrors Python VectorStoreService.delete_collection.
func (s *Store) DeleteCollection(projectPath string) error {
	if err := s.db.DeleteCollection(collectionName(projectPath)); err != nil {
		return fmt.Errorf("vectorstore delete collection: %w", err)
	}
	return nil
}

// Count returns the number of chunks stored for a project.
func (s *Store) Count(projectPath string) int {
	col := s.db.GetCollection(collectionName(projectPath), nil)
	if col == nil {
		return 0
	}
	return col.Count()
}

// round4 rounds f to 4 decimal places, matching Python's round(score, 4).
func round4(f float32) float32 {
	return float32(math.Round(float64(f)*10000) / 10000)
}

// ---------------------------------------------------------------------------
// Workspaces feature — centroid collection helpers (PR5).
//
// Each workspace gets a dedicated chromem collection holding ONE document
// per community: the mean-pooled, L2-normalised embedding of that
// community's member chunks. Stage 1 of two-stage workspace search hits
// this collection; stage 2 fans out to per-project collections to fetch
// the actual chunks.
//
// Collection naming is "ws_{md5hex(workspace_id)}_centroids" — md5 of the
// workspace id keeps the chromem on-disk name short and ASCII-safe even
// if the id (ULID/UUID today) changes shape later.
// ---------------------------------------------------------------------------

// CentroidDoc is the input unit for UpsertCentroids. Embedding is the
// pre-computed centroid vector — must be the same dimensionality as the
// per-chunk embeddings stage 2 will compare against.
type CentroidDoc struct {
	CommunityID  string
	Label        string
	ProjectPaths []string // pipe-joined into metadata for filter at search time
	MemberCount  int
	Embedding    []float32
}

// CentroidResult is what Search… returns from the centroid collection.
type CentroidResult struct {
	CommunityID  string
	Label        string
	ProjectPaths []string
	MemberCount  int
	Score        float32 // cosine similarity, [0,1]
}

func centroidCollectionName(workspaceID string) string {
	h := md5.Sum([]byte(workspaceID))
	return fmt.Sprintf("ws_%x_centroids", h)
}

// CentroidCollectionName is the exported alias used by dashboards / tests.
func CentroidCollectionName(workspaceID string) string {
	return centroidCollectionName(workspaceID)
}

func (s *Store) getOrCreateCentroidCollection(workspaceID string) (*chromem.Collection, error) {
	return s.db.GetOrCreateCollection(
		centroidCollectionName(workspaceID),
		map[string]string{"hnsw:space": "cosine"},
		embedNotUsed,
	)
}

// ReplaceCentroids drops every prior document in the workspace's centroid
// collection and writes the given docs. Communities are rebuilt
// wholesale (delete + reinsert) at the SQL layer too, so the two stores
// stay in sync without partial-update complexity.
func (s *Store) ReplaceCentroids(ctx context.Context, workspaceID string, docs []CentroidDoc) error {
	// Drop + recreate the collection — cheap because centroids are
	// O(#communities), typically <500 per workspace.
	_ = s.db.DeleteCollection(centroidCollectionName(workspaceID))
	if len(docs) == 0 {
		return nil
	}
	col, err := s.getOrCreateCentroidCollection(workspaceID)
	if err != nil {
		return err
	}
	chromemDocs := make([]chromem.Document, len(docs))
	for i, d := range docs {
		chromemDocs[i] = chromem.Document{
			ID:      d.CommunityID,
			Content: d.Label,
			Metadata: map[string]string{
				"community_id":  d.CommunityID,
				"label":         d.Label,
				"project_paths": strings.Join(d.ProjectPaths, "|"),
				"member_count":  strconv.Itoa(d.MemberCount),
			},
			Embedding: d.Embedding,
		}
	}
	for i := 0; i < len(chromemDocs); i += upsertBatchSize {
		end := i + upsertBatchSize
		end = min(end, len(chromemDocs))
		if err := col.AddDocuments(ctx, chromemDocs[i:end], 1); err != nil {
			return fmt.Errorf("vectorstore centroids batch [%d:%d]: %w", i, end, err)
		}
	}
	return nil
}

// SearchCentroids runs a top-K nearest-neighbor query against the
// workspace's centroid collection. Returns nil-slice on an empty
// workspace so callers can range over the result unconditionally.
func (s *Store) SearchCentroids(ctx context.Context, workspaceID string, queryEmbedding []float32, limit int) ([]CentroidResult, error) {
	col := s.db.GetCollection(centroidCollectionName(workspaceID), nil)
	if col == nil {
		return nil, nil
	}
	count := col.Count()
	if count == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	limit = min(limit, count)
	results, err := col.QueryEmbedding(ctx, queryEmbedding, limit, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("centroid search: %w", err)
	}
	out := make([]CentroidResult, len(results))
	for i, r := range results {
		mc, _ := strconv.Atoi(r.Metadata["member_count"])
		projPaths := []string{}
		if raw := r.Metadata["project_paths"]; raw != "" {
			projPaths = strings.Split(raw, "|")
		}
		out[i] = CentroidResult{
			CommunityID:  r.Metadata["community_id"],
			Label:        r.Metadata["label"],
			ProjectPaths: projPaths,
			MemberCount:  mc,
			Score:        round4(r.Similarity),
		}
	}
	return out, nil
}

// DeleteCentroids drops the workspace's centroid collection. Called from
// the workspace-delete handler.
func (s *Store) DeleteCentroids(workspaceID string) error {
	if err := s.db.DeleteCollection(centroidCollectionName(workspaceID)); err != nil {
		return fmt.Errorf("delete centroids: %w", err)
	}
	return nil
}

// FetchProjectChunkEmbeddings reads the raw stored embeddings for chunks
// whose symbol_name is in names. Used by communities.Build to mean-pool
// member embeddings without re-running the embedding model.
//
// chromem's `where` filter is single-equality, so we make one
// QueryEmbedding call per name. The query vector is the centroid we're
// trying to BUILD, which we don't have yet — so we pass a zero-vector
// stand-in. chromem still applies the filter correctly; the returned
// ordering is by cosine similarity to zero, which is uninformative but
// also irrelevant since we average all results.
//
// nResults is capped at 200 per name — a single symbol with >200 chunks
// is pathological (massive function); the centroid drift from sampling
// is negligible at that scale.
func (s *Store) FetchProjectChunkEmbeddings(ctx context.Context, projectPath string, names []string) ([][]float32, error) {
	col := s.db.GetCollection(collectionName(projectPath), nil)
	if col == nil {
		return nil, nil
	}
	count := col.Count()
	if count == 0 {
		return nil, nil
	}
	out := [][]float32{}
	if len(names) == 0 {
		return out, nil
	}
	// We need the embedding dimensionality to construct the dummy query.
	// Probing the first document is the cheapest way without leaking a
	// configuration knob into this package.
	results, err := col.QueryEmbedding(ctx, dummyEmbedding(384), 1, nil, nil)
	if err != nil || len(results) == 0 {
		return out, nil
	}
	dim := len(results[0].Embedding)
	if dim == 0 {
		return out, nil
	}
	query := dummyEmbedding(dim)

	for _, n := range names {
		limit := 200
		if limit > count {
			limit = count
		}
		docs, qerr := col.QueryEmbedding(ctx, query, limit, map[string]string{"symbol_name": n}, nil)
		if qerr != nil {
			continue
		}
		for _, d := range docs {
			if len(d.Embedding) > 0 {
				out = append(out, d.Embedding)
			}
		}
	}
	return out, nil
}

// dummyEmbedding returns a zero-vector of the given dimensionality.
// Used only as a placeholder query when we're really after the filter.
func dummyEmbedding(dim int) []float32 {
	return make([]float32, dim)
}
