package engine

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// SQLiteStore handles symbol storage and trigram full-text search.
type SQLiteStore struct {
	db *sql.DB
}

// SymbolRow is a record returned from SQLite queries.
type SymbolRow struct {
	ID         int64
	Name       string
	Kind       string
	File       string
	Line       int
	Content    string
	Hash       string
	ParentName string
}

// NewSQLiteStore opens (or creates) the SQLite database and ensures tables exist.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}

	if err := createSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	// Migration: add parent_name if upgrading from older schema (no-op if already present).
	db.Exec(`ALTER TABLE symbols ADD COLUMN parent_name TEXT NOT NULL DEFAULT ''`)

	return &SQLiteStore{db: db}, nil
}

func createSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS symbols (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT NOT NULL,
			kind        TEXT NOT NULL,
			file        TEXT NOT NULL,
			line        INTEGER NOT NULL,
			content     TEXT NOT NULL,
			hash        TEXT NOT NULL UNIQUE,
			parent_name TEXT NOT NULL DEFAULT ''
		);

		CREATE INDEX IF NOT EXISTS idx_symbols_name ON symbols(name);
		CREATE INDEX IF NOT EXISTS idx_symbols_file ON symbols(file);

		CREATE VIRTUAL TABLE IF NOT EXISTS symbols_fts USING fts5(
			name,
			kind,
			file,
			content,
			tokenize='trigram'
		);

		CREATE TRIGGER IF NOT EXISTS symbols_ai
		AFTER INSERT ON symbols BEGIN
			INSERT INTO symbols_fts(rowid, name, kind, file, content)
			VALUES (new.id, new.name, new.kind, new.file, new.content);
		END;

		CREATE TRIGGER IF NOT EXISTS symbols_au
		AFTER UPDATE ON symbols BEGIN
			UPDATE symbols_fts SET
				name    = new.name,
				kind    = new.kind,
				file    = new.file,
				content = new.content
			WHERE rowid = new.id;
		END;

		CREATE TRIGGER IF NOT EXISTS symbols_ad
		AFTER DELETE ON symbols BEGIN
			DELETE FROM symbols_fts WHERE rowid = old.id;
		END;

		CREATE TABLE IF NOT EXISTS files (
			path TEXT PRIMARY KEY,
			hash TEXT NOT NULL
		);
	`)
	return err
}

// Upsert inserts or updates a chunk by its content hash.
func (s *SQLiteStore) Upsert(chunk Chunk, hash string) error {
	// Check if hash already exists
	var id int64
	err := s.db.QueryRow(`SELECT id FROM symbols WHERE hash = ?`, hash).Scan(&id)
	if err == nil {
		// Already indexed, update in case file moved
		_, err = s.db.Exec(
			`UPDATE symbols SET name=?, kind=?, file=?, line=?, content=?, parent_name=? WHERE id=?`,
			chunk.Name, chunk.Kind, chunk.File, chunk.Line, chunk.Content, chunk.ParentName, id,
		)
		return err
	}

	_, err = s.db.Exec(
		`INSERT INTO symbols (name, kind, file, line, content, hash, parent_name) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		chunk.Name, chunk.Kind, chunk.File, chunk.Line, chunk.Content, hash, chunk.ParentName,
	)
	return err
}

// SearchSymbol finds symbols whose name matches the query string (case-insensitive prefix/exact).
func (s *SQLiteStore) SearchSymbol(query string, limit int) ([]SymbolRow, error) {
	rows, err := s.db.Query(
		`SELECT id, name, kind, file, line, content, parent_name FROM symbols
		 WHERE lower(name) LIKE lower(?) ORDER BY name LIMIT ?`,
		"%"+query+"%", limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// SearchTrigram uses FTS5 trigram index for substring matching.
func (s *SQLiteStore) SearchTrigram(query string, limit int) ([]SymbolRow, error) {
	rows, err := s.db.Query(
		`SELECT s.id, s.name, s.kind, s.file, s.line, s.content, s.parent_name
		 FROM symbols_fts f
		 JOIN symbols s ON s.id = f.rowid
		 WHERE symbols_fts MATCH ?
		 ORDER BY rank LIMIT ?`,
		query, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

func scanRows(rows *sql.Rows) ([]SymbolRow, error) {
	var results []SymbolRow
	for rows.Next() {
		var r SymbolRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.File, &r.Line, &r.Content, &r.ParentName); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// ListFiles returns all file paths currently tracked in the index.
func (s *SQLiteStore) ListFiles() ([]string, error) {
	rows, err := s.db.Query(`SELECT path FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		files = append(files, path)
	}
	return files, rows.Err()
}

// GetFileHash returns the stored hash for a file, and whether it was found.
func (s *SQLiteStore) GetFileHash(path string) (string, bool, error) {
	var hash string
	err := s.db.QueryRow(`SELECT hash FROM files WHERE path = ?`, path).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return hash, true, nil
}

// UpsertFileHash stores or updates the hash for a file.
func (s *SQLiteStore) UpsertFileHash(path, hash string) error {
	_, err := s.db.Exec(
		`INSERT INTO files (path, hash) VALUES (?, ?)
		 ON CONFLICT(path) DO UPDATE SET hash = excluded.hash`,
		path, hash,
	)
	return err
}

// DeleteByFile removes all symbols and the file entry for the given path.
func (s *SQLiteStore) DeleteByFile(file string) error {
	if _, err := s.db.Exec(`DELETE FROM symbols WHERE file = ?`, file); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM files WHERE path = ?`, file)
	return err
}

// Clear removes all symbols and file tracking data.
func (s *SQLiteStore) Clear() error {
	_, err := s.db.Exec(`DELETE FROM symbols; DELETE FROM files`)
	return err
}

// Close closes the underlying database.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
