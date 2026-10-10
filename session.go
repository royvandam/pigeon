package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

const sessionLifetime = 12 * time.Hour

type authentication struct {
	User      user
	CSRFToken string
	TokenHash string
	Basic     bool
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (s *store) createSession(ctx context.Context, u user) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	csrf, err := randomToken()
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
		tokenHash(token), csrf, time.Now().Add(sessionLifetime).Unix(), u.ID, u.Version)
	if err != nil {
		return "", err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		return "", errInvalidCredentials
	}
	return token, nil
}

func (s *store) session(ctx context.Context, token string) (authentication, error) {
	if len(token) != 43 {
		return authentication{}, errInvalidCredentials
	}
	var a authentication
	a.TokenHash = tokenHash(token)
	err := s.db.QueryRowContext(ctx, `
SELECT u.id, u.username, u.slug, u.is_admin, u.auth_version, u.quota_bytes, s.csrf_token
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = ? AND s.expires_at > ? AND u.deleted_at IS NULL AND s.user_version = u.auth_version`,
		a.TokenHash, time.Now().Unix()).
		Scan(&a.User.ID, &a.User.Username, &a.User.Slug, &a.User.Admin, &a.User.Version, &a.User.QuotaBytes, &a.CSRFToken)
	if errors.Is(err, sql.ErrNoRows) {
		return authentication{}, errInvalidCredentials
	}
	return a, err
}

func (s *store) deleteSession(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hash)
	return err
}
