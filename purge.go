package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Keep the account and its folder reserved until all uploads have been removed.
// The shared lock also serializes purging with recycling and outstanding uploads.
func (cfg config) purgeUser(ctx context.Context, id int64) error {
	unlock, err := cfg.store.lockUploads(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	tx, err := cfg.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var slug string
	err = tx.QueryRowContext(ctx, `DELETE FROM users WHERE id = ? AND deleted_at IS NOT NULL RETURNING slug`, id).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return errDeletedUserNotFound
	}
	if err != nil {
		return err
	}
	// Require one safe component, even if a stored slug was edited outside the app.
	if err := validateStoragePath(slug, false); err != nil || strings.Contains(slug, "/") {
		return fmt.Errorf("%w: invalid account folder", errStoragePath)
	}
	root, err := os.OpenRoot(cfg.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		defer root.Close()
		// RemoveAll unlinks symbolic links without following their targets.
		if err := root.RemoveAll(slug); err != nil {
			return err
		}
	}
	// Foreign keys remove any remaining sessions and shares. AUTOINCREMENT keeps
	// the old ID from being assigned to a new account with the same username.
	return tx.Commit()
}
