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
