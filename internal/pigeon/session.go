package pigeon

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/account"
)

const sessionLifetime = 12 * time.Hour

type authentication struct {
	User      account.User
	CSRFToken string
	TokenHash string
	Basic     bool
}

func (s *store) createSession(ctx context.Context, u account.User) (string, error) {
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
	// Password resets must also invalidate logins that were already in progress.
	result, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (token_hash, user_id, user_version, csrf_token, expires_at)
SELECT ?, id, auth_version, ?, ? FROM users WHERE id = ? AND auth_version = ? AND deleted_at IS NULL`,
		token.Hash(secret), csrf, time.Now().Add(sessionLifetime).Unix(), u.ID, u.Version)
	if err != nil {
		return "", err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		return "", account.ErrInvalidCredentials
	}
	return secret, nil
}

func (s *store) session(ctx context.Context, secret string) (authentication, error) {
	if len(secret) != token.Length {
		return authentication{}, account.ErrInvalidCredentials
	}
	var a authentication
	a.TokenHash = token.Hash(secret)
	err := s.db.QueryRowContext(ctx, `
SELECT u.id, u.username, u.slug, u.is_admin, u.auth_version, u.quota_bytes, s.csrf_token
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = ? AND s.expires_at > ? AND u.deleted_at IS NULL AND s.user_version = u.auth_version`,
		a.TokenHash, time.Now().Unix()).
		Scan(&a.User.ID, &a.User.Username, &a.User.Slug, &a.User.Admin, &a.User.Version, &a.User.QuotaBytes, &a.CSRFToken)
	if errors.Is(err, sql.ErrNoRows) {
		return authentication{}, account.ErrInvalidCredentials
	}
	return a, err
}

func (s *store) deleteSession(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hash)
	return err
}
