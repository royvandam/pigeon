package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	_ "modernc.org/sqlite"
)

type store struct {
	db          *sql.DB
	uploadLocks sync.Map
	progress    sync.Map
}

type settings struct {
	dir    string
	listen string
}

func (s settings) validate() error {
	if s.dir == "" {
		return fmt.Errorf("upload directory cannot be empty")
	}
	_, port, err := net.SplitHostPort(s.listen)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("invalid listen port: %w", err)
	}
	return nil
}

func openStore(ctx context.Context, path string) (*store, error) {
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
	s := &store{db: db}
	if err := s.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) initialize(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  uploads_dir TEXT NOT NULL,
  listen TEXT NOT NULL
);
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
`, defaultUserQuota))
	if err != nil {
		return err
	}
	var hasQuota, hasUploadLimit bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('users') WHERE name = 'quota_bytes')`).Scan(&hasQuota); err != nil {
		return err
	}
	if !hasQuota {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE users ADD COLUMN quota_bytes INTEGER NOT NULL DEFAULT %d CHECK (quota_bytes > 0)`, defaultUserQuota)); err != nil {
			return err
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('settings') WHERE name = 'max_bytes')`).Scan(&hasUploadLimit); err != nil {
		return err
	}
	if hasUploadLimit {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE settings DROP COLUMN max_bytes`); err != nil {
			return err
		}
	}
	dir, err := filepath.Abs("./uploads")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO settings (id, uploads_dir, listen) VALUES (1, ?, ?)`,
		dir, "0.0.0.0:8080"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *store) loadSettings(ctx context.Context) (settings, error) {
	var cfg settings
	err := s.db.QueryRowContext(ctx, `SELECT uploads_dir, listen FROM settings WHERE id = 1`).
		Scan(&cfg.dir, &cfg.listen)
	if err != nil {
		return cfg, err
	}
	return cfg, cfg.validate()
}

func (s *store) saveSettings(ctx context.Context, cfg settings) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE settings SET uploads_dir = ?, listen = ? WHERE id = 1`,
		cfg.dir, cfg.listen)
	return err
}
