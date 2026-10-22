// Package share stores public download links for files and folders. Anyone
// with a link's token can download its item without signing in, until the
// owner replaces or revokes it, or the item's path changes.
//
// Only a hash of each token is stored, like session tokens, so a leaked
// database or backup yields no working links, and an existing link's token
// cannot be shown again.
package share

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/account"
)

// ErrNotFound reports a token that matches no link of an active account.
var ErrNotFound = errors.New("share link not found")

// Link is a shared item. Its User has only an ID and a Slug.
type Link struct {
	User      account.User
	Path      string
	Directory bool
}

// Store keeps share links in the database opened by package database.
type Store struct {
	db *sql.DB
}

// NewStore keeps share links in db.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Active reports whether u's item at name has a link.
func (s *Store) Active(ctx context.Context, u account.User, name string) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM shares s JOIN users u ON u.id = s.user_id
WHERE s.user_id = ? AND s.path = ? AND u.auth_version = ? AND u.deleted_at IS NULL)`,
		u.ID, name, u.Version).Scan(&active)
	return active, err
}

// Create returns the token of a new link for u's item at name, replacing any
// existing link, which then stops working. It fails with
// account.ErrInvalidCredentials if u was deleted or its password changed since
// u was read.
func (s *Store) Create(ctx context.Context, u account.User, name string, directory bool) (string, error) {
	secret, err := token.New()
	if err != nil {
		return "", err
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO shares (token_hash, user_id, path, is_directory, created_at)
SELECT ?, id, ?, ?, ? FROM users WHERE id = ? AND auth_version = ? AND deleted_at IS NULL
ON CONFLICT(user_id, path) DO UPDATE SET
  token_hash = excluded.token_hash, is_directory = excluded.is_directory, created_at = excluded.created_at`,
		token.Hash(secret), name, directory, time.Now().Unix(), u.ID, u.Version)
	if err != nil {
		return "", err
	}
	if n, err := result.RowsAffected(); err != nil {
		return "", err
	} else if n == 0 {
		return "", account.ErrInvalidCredentials
	}
	return secret, nil
}

// Revoke removes the link of account id's item at name.
func (s *Store) Revoke(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE user_id = ? AND path = ?`, id, name)
	return err
}

// RevokePath removes the links of account id's item at name and of everything
// below it, before the item is renamed, moved, or deleted.
func (s *Store) RevokePath(ctx context.Context, id int64, name string) error {
	// Compare an exact path prefix; filenames may contain SQL LIKE wildcards.
	_, err := s.db.ExecContext(ctx, `
DELETE FROM shares WHERE user_id = ? AND (path = ? OR substr(path, 1, length(?) + 1) = ? || '/')`,
		id, name, name, name)
	return err
}

// Find returns the link with token secret, or ErrNotFound.
func (s *Store) Find(ctx context.Context, secret string) (Link, error) {
	if len(secret) != token.Length {
		return Link{}, ErrNotFound
	}
	bytes, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(bytes) != 32 {
		return Link{}, ErrNotFound
	}
	var link Link
	err = s.db.QueryRowContext(ctx, `
SELECT u.id, u.slug, s.path, s.is_directory FROM shares s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = ? AND u.deleted_at IS NULL`, token.Hash(secret)).
		Scan(&link.User.ID, &link.User.Slug, &link.Path, &link.Directory)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	return link, err
}
