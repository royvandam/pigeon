// Package database opens Pigeon's SQLite database and creates its schema. The
// database holds accounts, sessions, and share links; the stores in other
// packages read and change them through the returned *sql.DB.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Configuration chooses the database.
type Configuration struct {
	// Path is the SQLite database file. A relative path is resolved from the
	// working directory at startup.
	Path string `json:"path"`
}

// Validate checks cfg without creating anything.
func (cfg Configuration) Validate() error {
	if cfg.Path == "" {
		return errors.New("database path cannot be empty")
	}
	return nil
}

// IsUniqueViolation reports whether err is a statement that failed because a
// row would duplicate a UNIQUE column, so that stores need not know the driver.
func IsUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

// Open opens the SQLite database that cfg names, creating it and its parent
// folders if needed, and creates its schema in a new database. A relative path
// is resolved from the current working directory.
func Open(ctx context.Context, cfg Configuration) (*sql.DB, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	path, err := filepath.Abs(cfg.Path)
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
	if err := initialize(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// SchemaVersion is the version of the schema this release uses, kept in the
// database as PRAGMA user_version. A release that changes the schema raises it
// and converts databases from each earlier version in numbered steps.
const SchemaVersion = 1

const schema = `
CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL,
  username_key TEXT NOT NULL UNIQUE,
  slug TEXT NOT NULL UNIQUE,
  password_hash BLOB NOT NULL,
  is_admin INTEGER NOT NULL CHECK (is_admin IN (0, 1)),
  auth_version INTEGER NOT NULL DEFAULT 0,
  quota_bytes INTEGER NOT NULL CHECK (quota_bytes > 0),
  deleted_at INTEGER
);
CREATE TABLE sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  user_version INTEGER NOT NULL,
  csrf_token TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX sessions_user_id ON sessions(user_id);
CREATE INDEX sessions_expires_at ON sessions(expires_at);
CREATE TABLE shares (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  is_directory INTEGER NOT NULL CHECK (is_directory IN (0, 1)),
  created_at INTEGER NOT NULL,
  UNIQUE (user_id, path)
);
`

// initialize creates the schema in a new database, and checks that an existing
// one has the schema of this release.
func initialize(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	switch {
	case version == SchemaVersion:
		return nil
	case version > SchemaVersion:
		return fmt.Errorf("the database has schema version %d, but this release supports version %d; use a later release, or restore a backup taken before the upgrade", version, SchemaVersion)
	case version > 0:
		return fmt.Errorf("the database has schema version %d, which this release cannot convert", version)
	}
	var tables int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		return err
	}
	if tables > 0 {
		return errors.New("the database has no schema version: it was created by a development build or another program; start with a new database")
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, SchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}
