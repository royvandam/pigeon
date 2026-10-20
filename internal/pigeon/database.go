package pigeon

import (
	"context"
	"database/sql"
	"errors"
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
	// uploadSlots lets one upload per account stream at a time; see lockUploadSlot.
	uploadSlots sync.Map
	progress    sync.Map
	authLimiter *authLimiter
}

type settings struct {
	dir            string
	listen         string
	trustedProxies string // comma-separated addresses and CIDR ranges
	publicURL      string // external origin, such as https://files.example.com
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
	if _, err := parseTrustedProxies(s.trustedProxies); err != nil {
		return err
	}
	_, err = parsePublicURL(s.publicURL)
	return err
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
	s := &store{db: db, authLimiter: newAuthLimiter()}
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
  listen TEXT NOT NULL,
  trusted_proxies TEXT NOT NULL DEFAULT '',
  public_url TEXT NOT NULL DEFAULT ''
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
CREATE TABLE IF NOT EXISTS shares (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  is_directory INTEGER NOT NULL CHECK (is_directory IN (0, 1)),
  created_at INTEGER NOT NULL,
  UNIQUE (user_id, path)
);
`, defaultUserQuota))
	if err != nil {
		return err
	}
	var hasQuota, hasUploadLimit, hasTrustedProxies, hasPublicURL bool
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
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('settings') WHERE name = 'trusted_proxies')`).Scan(&hasTrustedProxies); err != nil {
		return err
	}
	if !hasTrustedProxies {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE settings ADD COLUMN trusted_proxies TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('settings') WHERE name = 'public_url')`).Scan(&hasPublicURL); err != nil {
		return err
	}
	if !hasPublicURL {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE settings ADD COLUMN public_url TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if err := hashShareTokens(ctx, tx); err != nil {
		return err
	}
	dir, err := filepath.Abs("./uploads")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO settings (id, uploads_dir, listen) VALUES (1, ?, ?)`,
		dir, defaultListen); err != nil {
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
		var token string
		if err := rows.Scan(&token); err != nil {
			rows.Close()
			return err
		}
		tokens = append(tokens, token)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, token := range tokens {
		if _, err := tx.ExecContext(ctx, `UPDATE shares SET token = ? WHERE token = ?`, tokenHash(token), token); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE shares RENAME COLUMN token TO token_hash`)
	return err
}

func (s *store) loadSettings(ctx context.Context) (settings, error) {
	var cfg settings
	err := s.db.QueryRowContext(ctx, `SELECT uploads_dir, listen, trusted_proxies, public_url FROM settings WHERE id = 1`).
		Scan(&cfg.dir, &cfg.listen, &cfg.trustedProxies, &cfg.publicURL)
	if err != nil {
		return cfg, err
	}
	return cfg, cfg.validate()
}

func (s *store) saveSettings(ctx context.Context, cfg settings) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE settings SET uploads_dir = ?, listen = ?, trusted_proxies = ?, public_url = ? WHERE id = 1`,
		cfg.dir, cfg.listen, cfg.trustedProxies, cfg.publicURL)
	return err
}
