// Package auth authenticates the people who use Pigeon. Passwords are checked
// by an Authenticator, which limits failed attempts per client and the number
// of checks running at once. A browser sign-in creates a session, which
// Sessions stores as a hash of its token.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/account"
)

// SessionLifetime is how long a sign-in lasts.
const SessionLifetime = 12 * time.Hour

// Authentication is an authenticated request's identity. Requests with a
// session carry its CSRF token and token hash; Basic is set for requests that
// sent a password instead.
type Authentication struct {
	User      account.User
	CSRFToken string
	TokenHash string
	Basic     bool
}

// Sessions stores sign-in sessions in the database opened by package database.
type Sessions struct {
	db *sql.DB
}

// NewSessions keeps sessions in db.
func NewSessions(db *sql.DB) *Sessions {
	return &Sessions{db: db}
}

// Create starts a session for u and returns its token. It fails with
// account.ErrInvalidCredentials if u was deleted or its password changed since
// u was read, so a password reset also stops sign-ins already in progress.
func (s *Sessions) Create(ctx context.Context, u account.User) (string, error) {
	secret, err := token.New()
	if err != nil {
		return "", err
	}
	csrf, err := token.New()
	if err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix()); err != nil {
		return "", err
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (token_hash, user_id, user_version, csrf_token, expires_at)
SELECT ?, id, auth_version, ?, ? FROM users WHERE id = ? AND auth_version = ? AND deleted_at IS NULL`,
		token.Hash(secret), csrf, time.Now().Add(SessionLifetime).Unix(), u.ID, u.Version)
	if err != nil {
		return "", err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		return "", account.ErrInvalidCredentials
	}
	return secret, nil
}

// Lookup returns the identity of an unexpired session, or
// account.ErrInvalidCredentials.
func (s *Sessions) Lookup(ctx context.Context, secret string) (Authentication, error) {
	if len(secret) != token.Length {
		return Authentication{}, account.ErrInvalidCredentials
	}
	var a Authentication
	a.TokenHash = token.Hash(secret)
	err := s.db.QueryRowContext(ctx, `
SELECT u.id, u.username, u.slug, u.is_admin, u.auth_version, u.quota_bytes, s.csrf_token
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = ? AND s.expires_at > ? AND u.deleted_at IS NULL AND s.user_version = u.auth_version`,
		a.TokenHash, time.Now().Unix()).
		Scan(&a.User.ID, &a.User.Username, &a.User.Slug, &a.User.Admin, &a.User.Version, &a.User.QuotaBytes, &a.CSRFToken)
	if errors.Is(err, sql.ErrNoRows) {
		return Authentication{}, account.ErrInvalidCredentials
	}
	return a, err
}

// Delete ends the session whose token has hash tokenHash.
func (s *Sessions) Delete(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}
