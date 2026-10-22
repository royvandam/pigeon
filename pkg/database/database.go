// Package database opens Pigeon's SQLite database and keeps its schema up to
// date. The database holds accounts, sessions, and share links; the stores in
// other packages read and change them through the returned *sql.DB.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/quota"
)

// Configuration chooses the database.
type Configuration struct {
	// Path is the SQLite database file. A relative path is resolved from the
	// working directory at startup.
	Path string `json:"path"`
}

// Open opens the SQLite database at path, creating it and its parent folders
// if needed, and migrates its schema. A relative path is resolved from the
// current working directory.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("database path cannot be empty")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String() +
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection serializes account changes and their transactions.
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL,
  username_key TEXT NOT NULL UNIQUE,
  slug TEXT NOT NULL UNIQUE,
  password_hash BLOB NOT NULL,
  is_admin INTEGER NOT NULL CHECK (is_admin IN (0, 1)),
  auth_version INTEGER NOT NULL DEFAULT 0,
  quota_bytes INTEGER NOT NULL DEFAULT %d CHECK (quota_bytes > 0),
  deleted_at INTEGER
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  user_version INTEGER NOT NULL,
  csrf_token TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS sessions_expires_at ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS shares (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  is_directory INTEGER NOT NULL CHECK (is_directory IN (0, 1)),
  created_at INTEGER NOT NULL,
  UNIQUE (user_id, path)
);
`, quota.DefaultQuota))
	if err != nil {
		return err
	}
	var hasQuota bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('users') WHERE name = 'quota_bytes')`).Scan(&hasQuota); err != nil {
		return err
	}
	if !hasQuota {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE users ADD COLUMN quota_bytes INTEGER NOT NULL DEFAULT %d CHECK (quota_bytes > 0)`, quota.DefaultQuota)); err != nil {
			return err
		}
	}
	if err := hashShareTokens(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// hashShareTokens replaces plaintext share tokens from older databases with
// their hashes, so existing links keep working but the database no longer
// contains them.
func hashShareTokens(ctx context.Context, tx *sql.Tx) error {
	var plaintext bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('shares') WHERE name = 'token')`).Scan(&plaintext); err != nil || !plaintext {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT token FROM shares`)
	if err != nil {
		return err
	}
	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		tokens = append(tokens, t)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, t := range tokens {
		if _, err := tx.ExecContext(ctx, `UPDATE shares SET token = ? WHERE token = ?`, token.Hash(t), t); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE shares RENAME COLUMN token TO token_hash`)
	return err
}
