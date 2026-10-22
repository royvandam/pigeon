package database

import (
	"context"
	"database/sql"
	"errors"
)

// LegacySettings are the server settings that versions before configuration
// files saved in the database, and that now belong in the configuration file.
type LegacySettings struct {
	UploadDirectory string
	ListenAddress   string
	TrustedProxies  string // comma-separated addresses and CIDR ranges
	PublicURL       string
}

// ReadLegacySettings returns the server settings an earlier version saved in
// db, or nil if there are none. Columns added by later versions read as empty
// when a database predates them.
func ReadLegacySettings(ctx context.Context, db *sql.DB) (*LegacySettings, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'settings')`).Scan(&exists); err != nil || !exists {
		return nil, err
	}
	columns := `uploads_dir, listen`
	for _, column := range []string{"trusted_proxies", "public_url"} {
		var present bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('settings') WHERE name = ?)`, column).Scan(&present); err != nil {
			return nil, err
		}
		if present {
			columns += ", " + column
		} else {
			columns += ", ''"
		}
	}
	var s LegacySettings
	err := db.QueryRowContext(ctx, `SELECT `+columns+` FROM settings WHERE id = 1`).
		Scan(&s.UploadDirectory, &s.ListenAddress, &s.TrustedProxies, &s.PublicURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// DropLegacySettings removes the server settings an earlier version saved in
// db, once the configuration file holds them.
func DropLegacySettings(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS settings`)
	return err
}
