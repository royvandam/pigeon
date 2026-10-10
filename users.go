package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"modernc.org/sqlite"
	"modernc.org/sqlite/lib"
)

var (
	errInvalidCredentials  = errors.New("invalid username or password")
	errUserConflict        = errors.New("username or upload folder is already reserved")
	errUserNotFound        = errors.New("user not found")
	errDeleteSelf          = errors.New("you cannot delete your own account")
	errLastAdmin           = errors.New("the last administrator cannot be deleted")
	errDeletedUserNotFound = errors.New("deleted account not found; it may already have been recycled or purged")
)

type user struct {
	ID         int64
	Username   string
	Slug       string
	Admin      bool
	Version    int64
	QuotaBytes int64
	Deleted    bool
}

// Username keys are case-insensitive; slugs also remove accents and separators.
func normalizeUsername(username string) (display, key, slug string, err error) {
	display = norm.NFKC.String(strings.TrimSpace(username))
	if !utf8.ValidString(display) || utf8.RuneCountInString(display) == 0 || utf8.RuneCountInString(display) > 64 {
		return "", "", "", fmt.Errorf("username must contain 1 to 64 characters")
	}
	for _, r := range display {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return "", "", "", fmt.Errorf("username cannot contain control or invisible formatting characters")
		}
	}
	key = norm.NFKC.String(cases.Fold().String(display))
	var normalized strings.Builder
	separator := false
	for _, r := range norm.NFKD.String(key) {
		switch {
		case unicode.IsMark(r):
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if separator && normalized.Len() > 0 {
				normalized.WriteByte('-')
			}
			normalized.WriteRune(r)
			separator = false
		default:
			separator = true
		}
	}
	slug = normalized.String()
	if slug == "" {
		return "", "", "", fmt.Errorf("username must contain a letter or number")
	}
	if len(slug) > 240 {
		return "", "", "", fmt.Errorf("normalized username is too long for an upload folder")
	}
	return display, key, slug, nil
}

func validatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 8 || len(password) > 72 {
		return fmt.Errorf("password must have at least 8 characters and at most 72 bytes")
	}
	return nil
}

func hashPassword(password string) ([]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	return bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
}

func (s *store) hasUsers(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users)`).Scan(&exists)
	return exists, err
}

func (s *store) createUser(ctx context.Context, username, password string, admin bool) (user, error) {
	return s.createUserWithQuota(ctx, username, password, admin, defaultUserQuota)
}

func (s *store) createUserWithQuota(ctx context.Context, username, password string, admin bool, quota int64) (user, error) {
	if err := validateQuota(quota); err != nil {
		return user{}, err
	}
	display, key, slug, err := normalizeUsername(username)
	if err != nil {
		return user{}, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return user{}, err
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, username_key, slug, password_hash, is_admin, quota_bytes) VALUES (?, ?, ?, ?, ?, ?)`,
		display, key, slug, hash, admin, quota)
	if err != nil {
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
			return user{}, errUserConflict
		}
		return user{}, err
	}
	id, err := result.LastInsertId()
	return user{ID: id, Username: display, Slug: slug, Admin: admin, QuotaBytes: quota}, err
}

func (s *store) authenticate(ctx context.Context, username, password string) (user, error) {
	_, key, _, err := normalizeUsername(username)
	if err != nil || len(password) > 72 {
		return user{}, errInvalidCredentials
	}
	var u user
	var hash []byte
	err = s.db.QueryRowContext(ctx,
		`SELECT id, username, slug, is_admin, auth_version, quota_bytes, password_hash FROM users WHERE username_key = ? AND deleted_at IS NULL`, key).
		Scan(&u.ID, &u.Username, &u.Slug, &u.Admin, &u.Version, &u.QuotaBytes, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		// Match the work performed for an existing user's incorrect password.
		hash = []byte("$2a$10$7EqJtq98hPqEX7fNZaFWoO5uZRxBxgGhK8DckbuZuODGVdLmRMaeW")
	} else if err != nil {
		return user{}, err
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || u.ID == 0 {
		return user{}, errInvalidCredentials
	}
	return u, nil
}

func (s *store) listUsers(ctx context.Context) ([]user, error) {
	return s.readUsers(ctx, false)
}

func (s *store) listAdminUsers(ctx context.Context) ([]user, error) {
	return s.readUsers(ctx, true)
}

func (s *store) readUsers(ctx context.Context, includeDeleted bool) ([]user, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, slug, is_admin, auth_version, quota_bytes, deleted_at IS NOT NULL FROM users WHERE deleted_at IS NULL OR ? ORDER BY username_key`, includeDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []user
	for rows.Next() {
		var u user
		if err := rows.Scan(&u.ID, &u.Username, &u.Slug, &u.Admin, &u.Version, &u.QuotaBytes, &u.Deleted); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *store) deletedUserIDForUsername(ctx context.Context, username string) (int64, error) {
	_, _, slug, err := normalizeUsername(username)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE slug = ? AND deleted_at IS NOT NULL`, slug).Scan(&id)
	return id, err
}

func (s *store) recycleUser(ctx context.Context, id int64, password string) (user, error) {
	hash, err := hashPassword(password)
	if err != nil {
		return user{}, err
	}
	// Finish any work started before deletion before restoring this account.
	unlock, err := s.lockUploads(ctx, id)
	if err != nil {
		return user{}, err
	}
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return user{}, err
	}
	defer tx.Rollback()
	var u user
	err = tx.QueryRowContext(ctx, `
UPDATE users SET password_hash = ?, auth_version = auth_version + 1, deleted_at = NULL
WHERE id = ? AND deleted_at IS NOT NULL
RETURNING id, username, slug, is_admin, auth_version, quota_bytes`, hash, id).
		Scan(&u.ID, &u.Username, &u.Slug, &u.Admin, &u.Version, &u.QuotaBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return user{}, errDeletedUserNotFound
	}
	if err != nil {
		return user{}, err
	}
	for _, query := range []string{`DELETE FROM sessions WHERE user_id = ?`, `DELETE FROM shares WHERE user_id = ?`} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return user{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return user{}, err
	}
	return u, nil
}

func (s *store) resetPassword(ctx context.Context, id int64, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, auth_version = auth_version + 1 WHERE id = ? AND deleted_at IS NULL`, hash, id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		return errUserNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *store) deleteUser(ctx context.Context, id, actorID int64) error {
	if id == actorID {
		return errDeleteSelf
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var admin bool
	if err := tx.QueryRowContext(ctx, `SELECT is_admin FROM users WHERE id = ? AND deleted_at IS NULL`, id).Scan(&admin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errUserNotFound
		}
		return err
	}
	if admin {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1 AND deleted_at IS NULL`).Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			return errLastAdmin
		}
	}
	// Keep the username and slug reserved so preserved uploads stay isolated.
	if _, err := tx.ExecContext(ctx, `UPDATE users SET deleted_at = ?, password_hash = X'' WHERE id = ?`, time.Now().Unix(), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM shares WHERE user_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
