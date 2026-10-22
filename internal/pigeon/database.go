package pigeon

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/database"
	"github.com/royvandam/pigeon/pkg/server"
)

// store is the database, with its accounts and the server settings.
type store struct {
	db       *sql.DB
	accounts *account.Store
}

type settings struct {
	dir            string
	listen         string
	trustedProxies string // comma-separated addresses and CIDR ranges
	publicURL      string // external origin, such as https://files.example.com
}

// server returns the settings of the HTTP server.
func (s settings) server() server.Configuration {
	return server.Configuration{ListenAddress: s.listen, PublicURL: s.publicURL, TrustedProxies: strings.Split(s.trustedProxies, ",")}
}

func (s settings) validate() error {
	if s.dir == "" {
		return fmt.Errorf("upload directory cannot be empty")
	}
	return s.server().Validate()
}

// openStore opens or creates the database. passwordCost is the bcrypt cost for
// passwords stored from now on; existing hashes keep the cost they were made with.
func openStore(ctx context.Context, path string, passwordCost int) (*store, error) {
	db, err := database.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	accounts, err := account.NewStore(db, passwordCost)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &store{db: db, accounts: accounts}
	if err := s.initializeSettings(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) initializeSettings(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  uploads_dir TEXT NOT NULL,
  listen TEXT NOT NULL,
  trusted_proxies TEXT NOT NULL DEFAULT '',
  public_url TEXT NOT NULL DEFAULT ''
);
`)
	if err != nil {
		return err
	}
	var hasUploadLimit, hasTrustedProxies, hasPublicURL bool
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
	dir, err := filepath.Abs("./uploads")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO settings (id, uploads_dir, listen) VALUES (1, ?, ?)`,
		dir, server.DefaultListenAddress); err != nil {
		return err
	}
	return tx.Commit()
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
