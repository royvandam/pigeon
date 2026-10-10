package main

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	_ "modernc.org/sqlite"
)

type store struct {
	db *sql.DB
}

type settings struct {
	dir      string
	maxBytes int64
	listen   string
}

func (s settings) validate() error {
	if s.dir == "" {
		return fmt.Errorf("upload directory cannot be empty")
	}
	if s.maxBytes <= 0 || s.maxBytes > math.MaxInt64-multipartOverhead {
		return fmt.Errorf("upload limit must be positive and allow for multipart overhead")
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
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  uploads_dir TEXT NOT NULL,
  max_bytes INTEGER NOT NULL CHECK (max_bytes > 0),
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
`)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs("./uploads")
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO settings (id, uploads_dir, max_bytes, listen) VALUES (1, ?, ?, ?)`,
		dir, int64(200<<20), "0.0.0.0:8080")
	return err
}

func (s *store) loadSettings(ctx context.Context) (settings, error) {
	var cfg settings
	err := s.db.QueryRowContext(ctx, `SELECT uploads_dir, max_bytes, listen FROM settings WHERE id = 1`).
		Scan(&cfg.dir, &cfg.maxBytes, &cfg.listen)
	if err != nil {
		return cfg, err
	}
	return cfg, cfg.validate()
}

func (s *store) saveSettings(ctx context.Context, cfg settings) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE settings SET uploads_dir = ?, max_bytes = ?, listen = ? WHERE id = 1`,
		cfg.dir, cfg.maxBytes, cfg.listen)
	return err
}
