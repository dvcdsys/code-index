// Package symbolindex ports api/app/services/symbol_index.py and
// api/app/services/reference_index.py to Go using database/sql.
// Queries are byte-identical to the Python originals where possible.
package symbolindex

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// Symbol mirrors Python SymbolInfo.
type Symbol struct {
	ID          string
	ProjectPath string
	Name        string
	Kind        string // function|class|method|type
	FilePath    string
	Line        int
	EndLine     int
	Language    string
	Signature   *string
	ParentName  *string
	Docstring   *string
}

// Reference mirrors Python ReferenceInfo stored in the refs table.
type Reference struct {
	ProjectPath string
	Name        string
	FilePath    string
	Line        int
	Col         int
	Language    string
}

// ---------------------------------------------------------------------------
// Symbol CRUD
// ---------------------------------------------------------------------------

// UpsertSymbols inserts or replaces symbols for the given project.
// Mirrors SymbolIndexService.upsert_symbols in symbol_index.py.
func UpsertSymbols(ctx context.Context, db *sql.DB, projectPath string, symbols []Symbol) error {
	if len(symbols) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // safe no-op after commit

	if err := UpsertSymbolsTx(ctx, tx, projectPath, symbols); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// UpsertSymbolsTx is the Tx-scoped counterpart of UpsertSymbols. The caller
// owns the transaction (commit/rollback). Used by the indexer's batch tx.
func UpsertSymbolsTx(ctx context.Context, tx *sql.Tx, projectPath string, symbols []Symbol) error {
	for i := range symbols {
		if symbols[i].ID == "" {
			symbols[i].ID = uuid.NewString()
		}
		_, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO symbols
			 (id, project_path, name, kind, file_path, line, end_line, language, signature, parent_name, docstring)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			symbols[i].ID,
			projectPath,
			symbols[i].Name,
			symbols[i].Kind,
			symbols[i].FilePath,
			symbols[i].Line,
			symbols[i].EndLine,
			symbols[i].Language,
			symbols[i].Signature,
			symbols[i].ParentName,
			symbols[i].Docstring,
		)
		if err != nil {
			return fmt.Errorf("upsert symbol %q: %w", symbols[i].Name, err)
		}
	}
	return nil
}

// DeleteByFile removes all symbols for a specific file within a project.
// Mirrors SymbolIndexService.delete_by_file.
func DeleteByFile(ctx context.Context, db *sql.DB, projectPath, filePath string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM symbols WHERE project_path = ? AND file_path = ?`,
		projectPath, filePath,
	)
	return err
}

// DeleteByFileTx is the Tx-scoped counterpart of DeleteByFile. Used by the
// indexer to batch per-file deletes inside its outer SAVEPOINT so a failure
// on one file rolls back just that file's work.
func DeleteByFileTx(ctx context.Context, tx *sql.Tx, projectPath, filePath string) error {
	_, err := tx.ExecContext(ctx,
		`DELETE FROM symbols WHERE project_path = ? AND file_path = ?`,
		projectPath, filePath,
	)
	return err
}

// DeleteByProject removes all symbols for a project.
func DeleteByProject(ctx context.Context, db *sql.DB, projectPath string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM symbols WHERE project_path = ?`,
		projectPath,
	)
	return err
}

// SearchByName searches for symbols by name with exact → prefix → contains
// fallback strategy, matching SymbolIndexService.search in Python.
func SearchByName(ctx context.Context, db *sql.DB, projectPath, query string, kinds []string, limit int) ([]Symbol, error) {
	if limit <= 0 {
		limit = 20
	}

	for _, pattern := range []string{query, query + "%", "%" + query + "%"} {
		rows, err := querySymbols(ctx, db, projectPath, pattern, kinds, limit)
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			return rows, nil
		}
	}
	return nil, nil
}

// SearchDefinitions performs an exact-then-like lookup in the symbols table.
// Mirrors definition_search in search.py.
func SearchDefinitions(ctx context.Context, db *sql.DB, projectPath, symbol, kind, filePath string, limit int) ([]Symbol, error) {
	if limit <= 0 {
		limit = 10
	}

	// Exact match first.
	rows, err := queryDefinitions(ctx, db, projectPath, symbol, kind, filePath, false, limit)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		return rows, nil
	}

	// Case-insensitive LIKE fallback.
	return queryDefinitions(ctx, db, projectPath, symbol, kind, filePath, true, limit)
}

// GetProjectSymbols returns all symbols for a project ordered by kind, name.
// Mirrors SymbolIndexService.get_project_symbols.
func GetProjectSymbols(ctx context.Context, db *sql.DB, projectPath string) ([]Symbol, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, project_path, name, kind, file_path, line, end_line, language, signature, parent_name, docstring
		 FROM symbols WHERE project_path = ? ORDER BY kind, name`,
		projectPath,
	)
	if err != nil {
		return nil, fmt.Errorf("query symbols: %w", err)
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// CountProjectSymbols returns the symbol count for a project.
func CountProjectSymbols(ctx context.Context, db *sql.DB, projectPath string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM symbols WHERE project_path = ?`, projectPath,
	).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------------------
// Reference CRUD
// ---------------------------------------------------------------------------

// UpsertReferences inserts references (no ON CONFLICT — mirrors Python executemany INSERT).
func UpsertReferences(ctx context.Context, db *sql.DB, projectPath string, refs []Reference) error {
	if len(refs) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // safe no-op after commit

	if err := UpsertReferencesTx(ctx, tx, projectPath, refs); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// UpsertReferencesTx is the Tx-scoped counterpart of UpsertReferences.
func UpsertReferencesTx(ctx context.Context, tx *sql.Tx, projectPath string, refs []Reference) error {
	for _, r := range refs {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO refs (project_path, name, file_path, line, col, language)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			projectPath, r.Name, r.FilePath, r.Line, r.Col, r.Language,
		)
		if err != nil {
			return fmt.Errorf("insert ref %q: %w", r.Name, err)
		}
	}
	return nil
}

// DeleteRefsByFile removes refs for a specific file within a project.
// Mirrors ReferenceIndexService.delete_by_file.
func DeleteRefsByFile(ctx context.Context, db *sql.DB, projectPath, filePath string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM refs WHERE project_path = ? AND file_path = ?`,
		projectPath, filePath,
	)
	return err
}

// DeleteRefsByFileTx is the Tx-scoped counterpart of DeleteRefsByFile.
func DeleteRefsByFileTx(ctx context.Context, tx *sql.Tx, projectPath, filePath string) error {
	_, err := tx.ExecContext(ctx,
		`DELETE FROM refs WHERE project_path = ? AND file_path = ?`,
		projectPath, filePath,
	)
	return err
}

// DeleteRefsByProject removes all refs for a project.
// Mirrors ReferenceIndexService.delete_by_project.
func DeleteRefsByProject(ctx context.Context, db *sql.DB, projectPath string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM refs WHERE project_path = ?`,
		projectPath,
	)
	return err
}

// SearchReferences looks up usages of a symbol name within a project.
// Mirrors ReferenceIndexService.search.
func SearchReferences(ctx context.Context, db *sql.DB, projectPath, name, filePath string, limit int) ([]Reference, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `SELECT name, file_path, line, col, language FROM refs WHERE project_path = ? AND name = ?`
	args := []any{projectPath, name}

	if filePath != "" {
		query += " AND file_path = ?"
		args = append(args, filePath)
	}
	query += " ORDER BY file_path, line LIMIT ?"
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query refs: %w", err)
	}
	defer rows.Close()

	var out []Reference
	for rows.Next() {
		var r Reference
		r.ProjectPath = projectPath
		if err := rows.Scan(&r.Name, &r.FilePath, &r.Line, &r.Col, &r.Language); err != nil {
			return nil, fmt.Errorf("scan ref: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func querySymbols(ctx context.Context, db *sql.DB, projectPath, pattern string, kinds []string, limit int) ([]Symbol, error) {
	query := `SELECT id, project_path, name, kind, file_path, line, end_line, language, signature, parent_name, docstring
	          FROM symbols WHERE project_path = ? AND name LIKE ?`
	args := []any{projectPath, pattern}

	if len(kinds) > 0 {
		query += " AND kind IN (?" + repeatComma(len(kinds)-1) + ")"
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	query += " ORDER BY name LIMIT ?"
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query symbols: %w", err)
	}
	defer rows.Close()
	return scanSymbols(rows)
}

func queryDefinitions(ctx context.Context, db *sql.DB, projectPath, symbol, kind, filePath string, useLike bool, limit int) ([]Symbol, error) {
	var query string
	args := []any{projectPath, symbol}

	if useLike {
		query = `SELECT id, project_path, name, kind, file_path, line, end_line, language, signature, parent_name, docstring
		         FROM symbols WHERE project_path = ? AND name LIKE ?`
	} else {
		query = `SELECT id, project_path, name, kind, file_path, line, end_line, language, signature, parent_name, docstring
		         FROM symbols WHERE project_path = ? AND name = ?`
	}

	if kind != "" {
		query += " AND kind = ?"
		args = append(args, kind)
	}
	if filePath != "" {
		query += " AND file_path = ?"
		args = append(args, filePath)
	}
	query += " ORDER BY name LIMIT ?"
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query definitions: %w", err)
	}
	defer rows.Close()
	return scanSymbols(rows)
}

func scanSymbols(rows *sql.Rows) ([]Symbol, error) {
	var out []Symbol
	for rows.Next() {
		var s Symbol
		if err := rows.Scan(
			&s.ID, &s.ProjectPath, &s.Name, &s.Kind, &s.FilePath,
			&s.Line, &s.EndLine, &s.Language,
			&s.Signature, &s.ParentName, &s.Docstring,
		); err != nil {
			return nil, fmt.Errorf("scan symbol: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func repeatComma(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n*2)
	for i := range b {
		if i%2 == 0 {
			b[i] = ','
		} else {
			b[i] = '?'
		}
	}
	return string(b)
}
