package account

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/royvandam/pigeon/pkg/database"
	"github.com/royvandam/pigeon/pkg/quota"
)

// Store reads and changes accounts in the database opened by package database.
// It takes no locks: Manager coordinates changes that need them.
type Store struct {
	db        *sql.DB
	passwords passwordHasher
}

// NewStore keeps accounts in db. passwordCost is the bcrypt cost for passwords
// stored from now on; existing hashes keep the cost they were made with.
func NewStore(db *sql.DB, passwordCost int) (*Store, error) {
	passwords, err := newPasswordHasher(passwordCost)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, passwords: passwords}, nil
}

// HasUsers reports whether any account exists, deleted or not.
func (s *Store) HasUsers(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users)`).Scan(&exists)
	return exists, err
}

// Create adds an account. Its error wraps ErrUserConflict when the username or
// its slug is taken, including by a deleted account.
func (s *Store) Create(ctx context.Context, username, password string, admin bool, quotaBytes int64) (User, error) {
	if err := quota.Validate(quotaBytes); err != nil {
		return User{}, err
	}
	display, key, slug, err := NormalizeUsername(username)
	if err != nil {
		return User{}, err
	}
	hash, err := s.passwords.hash(password)
	if err != nil {
		return User{}, err
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, username_key, slug, password_hash, is_admin, quota_bytes) VALUES (?, ?, ?, ?, ?, ?)`,
		display, key, slug, hash, admin, quotaBytes)
	if err != nil {
		if database.IsUniqueViolation(err) {
			return User{}, ErrUserConflict
		}
		return User{}, err
	}
	id, err := result.LastInsertId()
	return User{ID: id, Username: display, Slug: slug, Admin: admin, QuotaBytes: quotaBytes}, err
}

// Authenticate returns the active account with username and password, or
// ErrInvalidCredentials. An unknown username takes as long to check as a wrong
// password.
func (s *Store) Authenticate(ctx context.Context, username, password string) (User, error) {
	_, key, _, err := NormalizeUsername(username)
	if err != nil || len(password) > 72 {
		return User{}, ErrInvalidCredentials
	}
	var u User
	var hash []byte
	err = s.db.QueryRowContext(ctx,
		`SELECT id, username, slug, is_admin, auth_version, quota_bytes, password_hash FROM users WHERE username_key = ? AND deleted_at IS NULL`, key).
		Scan(&u.ID, &u.Username, &u.Slug, &u.Admin, &u.Version, &u.QuotaBytes, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		hash = nil // Still spend the time of a wrong password.
	} else if err != nil {
		return User{}, err
	}
	if !s.passwords.matches(hash, password) {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}

// List returns every account, including deleted ones, by username.
func (s *Store) List(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, slug, is_admin, auth_version, quota_bytes, deleted_at IS NOT NULL FROM users ORDER BY username_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Slug, &u.Admin, &u.Version, &u.QuotaBytes, &u.Deleted); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// DeletedIDForUsername returns the ID of the deleted account that reserves
// username's slug, or ErrDeletedUserNotFound if none does.
func (s *Store) DeletedIDForUsername(ctx context.Context, username string) (int64, error) {
	_, _, slug, err := NormalizeUsername(username)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE slug = ? AND deleted_at IS NOT NULL`, slug).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrDeletedUserNotFound
	}
	return id, err
}

// Quota returns u's current quota, or ErrInvalidCredentials if u has been
// deleted or its credentials changed since it was read.
func (s *Store) Quota(ctx context.Context, u User) (int64, error) {
	var bytes int64
	err := s.db.QueryRowContext(ctx, `SELECT quota_bytes FROM users WHERE id = ? AND auth_version = ? AND deleted_at IS NULL`, u.ID, u.Version).Scan(&bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrInvalidCredentials
	}
	if err != nil {
		return 0, err
	}
	return bytes, quota.Validate(bytes)
}

// SetQuota changes an active account's quota.
func (s *Store) SetQuota(ctx context.Context, id, bytes int64) error {
	if err := quota.Validate(bytes); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE users SET quota_bytes = ? WHERE id = ? AND deleted_at IS NULL`, bytes, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// Recycle restores a deleted account with a new password, keeping its
// username, role, quota, and files. Its earlier sessions and share links are
// removed.
func (s *Store) Recycle(ctx context.Context, id int64, password string) (User, error) {
	hash, err := s.passwords.hash(password)
	if err != nil {
		return User{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var u User
	err = tx.QueryRowContext(ctx, `
UPDATE users SET password_hash = ?, auth_version = auth_version + 1, deleted_at = NULL
WHERE id = ? AND deleted_at IS NOT NULL
RETURNING id, username, slug, is_admin, auth_version, quota_bytes`, hash, id).
		Scan(&u.ID, &u.Username, &u.Slug, &u.Admin, &u.Version, &u.QuotaBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrDeletedUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	for _, query := range []string{`DELETE FROM sessions WHERE user_id = ?`, `DELETE FROM shares WHERE user_id = ?`} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return User{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}

// ResetPassword gives an active account a new password and signs it out
// everywhere.
func (s *Store) ResetPassword(ctx context.Context, id int64, password string) (User, error) {
	hash, err := s.passwords.hash(password)
	if err != nil {
		return User{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var u User
	err = tx.QueryRowContext(ctx, `
UPDATE users SET password_hash = ?, auth_version = auth_version + 1
WHERE id = ? AND deleted_at IS NULL
RETURNING id, username, slug, is_admin, auth_version, quota_bytes`, hash, id).
		Scan(&u.ID, &u.Username, &u.Slug, &u.Admin, &u.Version, &u.QuotaBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}

// Delete disables account id on behalf of actorID, removing its sessions and
// share links. Its username and slug stay reserved, so its preserved files
// stay isolated. An account cannot delete itself, and the last administrator
// cannot be deleted.
func (s *Store) Delete(ctx context.Context, id, actorID int64) error {
	if id == actorID {
		return ErrDeleteSelf
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var admin bool
	if err := tx.QueryRowContext(ctx, `SELECT is_admin FROM users WHERE id = ? AND deleted_at IS NULL`, id).Scan(&admin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		return err
	}
	if admin {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1 AND deleted_at IS NULL`).Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			return ErrLastAdmin
		}
	}
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

// Purge is the permanent removal of a deleted account, in progress. The
// account stays reserved until Commit, so its files can be removed first and a
// failure keeps the username from being reused.
type Purge struct {
	tx   *sql.Tx
	Slug string
}

// BeginPurge starts removing deleted account id. Call Rollback when the purge
// is not committed.
func (s *Store) BeginPurge(ctx context.Context, id int64) (*Purge, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	p := &Purge{tx: tx}
	err = tx.QueryRowContext(ctx, `DELETE FROM users WHERE id = ? AND deleted_at IS NOT NULL RETURNING slug`, id).Scan(&p.Slug)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrDeletedUserNotFound
	}
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return p, nil
}

// Commit removes the account. Foreign keys remove any remaining sessions and
// share links. AUTOINCREMENT keeps the old ID from being assigned to a new
// account with the same username.
func (p *Purge) Commit() error {
	return p.tx.Commit()
}

// Rollback keeps the account; it does nothing after Commit.
func (p *Purge) Rollback() error {
	return p.tx.Rollback()
}
