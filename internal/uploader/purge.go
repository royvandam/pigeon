package uploader

import (
	"context"
	"database/sql"
	"errors"
)

// Keep the account and its folder reserved until all uploads have been removed.
// The shared lock also serializes purging with recycling and outstanding uploads.
func (s *store) purgeUser(ctx context.Context, id int64, removeUploads func(string) error) error {
	unlock, err := s.lockUploads(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
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
	if err := removeUploads(slug); err != nil {
		return err
	}
	// Foreign keys remove any remaining sessions and shares. AUTOINCREMENT keeps
	// the old ID from being assigned to a new account with the same username.
	return tx.Commit()
}
