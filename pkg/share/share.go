// Package share stores public links to files and folders. Anyone with a
// download link's token can download its item without signing in, and anyone
// with an upload link's token can upload files into its folder, until the
// owner replaces or revokes the link, the link expires, or the item's path
// changes.
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
	"fmt"
	"time"

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/account"
)

var (
	// ErrNotFound reports a token that matches no current link of an active
	// account.
	ErrNotFound = errors.New("share link not found")
	// ErrInvalidLink reports a link that cannot be created as asked.
	ErrInvalidLink = errors.New("invalid share link")
)

// Kind is what a link lets anyone who has it do.
type Kind string

const (
	// Download links download a file, or a folder as a ZIP archive.
	Download Kind = "download"
	// Upload links upload files into a folder, without showing what it holds.
	Upload Kind = "upload"
)

// ParseKind returns the kind named name, or Download for "".
func ParseKind(name string) (Kind, error) {
	switch Kind(name) {
	case "", Download:
		return Download, nil
	case Upload:
		return Upload, nil
	}
	return "", fmt.Errorf("%w: unknown kind %q", ErrInvalidLink, name)
}

// MaxLifetime is the longest lifetime a link may have, other than none.
const MaxLifetime = 365 * 24 * time.Hour

// Link is a shared item. Its User has only an ID, a Slug, and a Version.
type Link struct {
	User      account.User
	Path      string
	Kind      Kind
	Directory bool
	// Expires is when the link stops working, or zero if it does not expire.
	Expires time.Time
}

// Status describes the link of an item.
type Status struct {
	Active bool
	// Expires is when an active link stops working, or zero if it does not
	// expire.
	Expires time.Time
}

// Store keeps share links in the database opened by package database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore keeps share links in db. now returns the current time, normally
// time.Now.
func NewStore(db *sql.DB, now func() time.Time) *Store {
	return &Store{db: db, now: now}
}

// Status describes the link of the given kind for u's item at name. An expired
// link is not active.
func (s *Store) Status(ctx context.Context, u account.User, name string, kind Kind) (Status, error) {
	var expires sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
SELECT s.expires_at FROM shares s JOIN users u ON u.id = s.user_id
WHERE s.user_id = ? AND s.path = ? AND s.kind = ? AND u.auth_version = ? AND u.deleted_at IS NULL
  AND (s.expires_at IS NULL OR s.expires_at > ?)`,
		u.ID, name, kind, u.Version, s.now().Unix()).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	return Status{Active: true, Expires: expiry(expires)}, nil
}

// Create returns the token of a new link of the given kind for u's item at
// name, and when it expires. It replaces any existing link of that kind for
// the item, which then stops working. A lifetime of zero creates a link that
// does not expire. Upload links need a folder. It fails with
// account.ErrInvalidCredentials if u was deleted or its password changed since
// u was read.
func (s *Store) Create(ctx context.Context, u account.User, name string, kind Kind, directory bool, lifetime time.Duration) (string, time.Time, error) {
	switch {
	case kind != Download && kind != Upload:
		return "", time.Time{}, fmt.Errorf("%w: unknown kind %q", ErrInvalidLink, kind)
	case kind == Upload && !directory:
		return "", time.Time{}, fmt.Errorf("%w: upload links need a folder", ErrInvalidLink)
	case lifetime < 0 || lifetime > MaxLifetime:
		return "", time.Time{}, fmt.Errorf("%w: a link may last at most %d days", ErrInvalidLink, MaxLifetime/(24*time.Hour))
	}
	secret, err := token.New()
	if err != nil {
		return "", time.Time{}, err
	}
	now := s.now()
	var expires sql.NullInt64
	if lifetime > 0 {
		expires = sql.NullInt64{Int64: now.Add(lifetime).Unix(), Valid: true}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE expires_at <= ?`, now.Unix()); err != nil {
		return "", time.Time{}, err
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO shares (token_hash, user_id, path, kind, is_directory, created_at, expires_at)
SELECT ?, id, ?, ?, ?, ?, ? FROM users WHERE id = ? AND auth_version = ? AND deleted_at IS NULL
ON CONFLICT(user_id, path, kind) DO UPDATE SET
  token_hash = excluded.token_hash, is_directory = excluded.is_directory,
  created_at = excluded.created_at, expires_at = excluded.expires_at`,
		token.Hash(secret), name, kind, directory, now.Unix(), expires, u.ID, u.Version)
	if err != nil {
		return "", time.Time{}, err
	}
	if n, err := result.RowsAffected(); err != nil {
		return "", time.Time{}, err
	} else if n == 0 {
		return "", time.Time{}, account.ErrInvalidCredentials
	}
	return secret, expiry(expires), nil
}

// Revoke removes the link of the given kind for account id's item at name.
func (s *Store) Revoke(ctx context.Context, id int64, name string, kind Kind) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE user_id = ? AND path = ? AND kind = ?`, id, name, kind)
	return err
}

// RevokePath removes every link of account id's item at name and of
// everything below it, before the item is renamed, moved, or deleted.
func (s *Store) RevokePath(ctx context.Context, id int64, name string) error {
	// Compare an exact path prefix; filenames may contain SQL LIKE wildcards.
	_, err := s.db.ExecContext(ctx, `
DELETE FROM shares WHERE user_id = ? AND (path = ? OR substr(path, 1, length(?) + 1) = ? || '/')`,
		id, name, name, name)
	return err
}

// Find returns the link with token secret, or ErrNotFound if there is none or
// it has expired.
func (s *Store) Find(ctx context.Context, secret string) (Link, error) {
	if len(secret) != token.Length {
		return Link{}, ErrNotFound
	}
	bytes, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(bytes) != 32 {
		return Link{}, ErrNotFound
	}
	var link Link
	var expires sql.NullInt64
	err = s.db.QueryRowContext(ctx, `
SELECT u.id, u.slug, u.auth_version, s.path, s.kind, s.is_directory, s.expires_at
FROM shares s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = ? AND u.deleted_at IS NULL AND (s.expires_at IS NULL OR s.expires_at > ?)`,
		token.Hash(secret), s.now().Unix()).
		Scan(&link.User.ID, &link.User.Slug, &link.User.Version, &link.Path, &link.Kind, &link.Directory, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	link.Expires = expiry(expires)
	return link, err
}

func expiry(unix sql.NullInt64) time.Time {
	if !unix.Valid {
		return time.Time{}
	}
	return time.Unix(unix.Int64, 0)
}
