// Package account manages Pigeon's accounts: their usernames, passwords,
// roles, and quotas, and their lifecycle from creation through deletion,
// recycling, and purging.
//
// Store reads and changes accounts in the database. Locks serializes the work
// done on each account, and Manager coordinates changes that also involve an
// account's files or its usage, taking the locks they need.
package account

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrInvalidCredentials    = errors.New("invalid username or password")
	ErrUserConflict          = errors.New("username or upload folder is already reserved")
	ErrUserNotFound          = errors.New("user not found")
	ErrDeleteSelf            = errors.New("you cannot delete your own account")
	ErrLastAdmin             = errors.New("the last administrator cannot be deleted")
	ErrDeletedUserNotFound   = errors.New("deleted account not found; it may already have been recycled or purged")
	ErrAdministratorRequired = errors.New("administrator access required")
	ErrPassphraseGeneration  = errors.New("failed to generate passphrase")
)

// User is an account as it was when it was read. Version changes whenever the
// account's credentials do, so a request can tell whether the identity it
// authenticated is still current.
type User struct {
	ID         int64
	Username   string
	Slug       string // the account's storage namespace
	Admin      bool
	Version    int64
	QuotaBytes int64
	Deleted    bool
}

// NormalizeUsername returns the username as displayed, the key that makes
// usernames case-insensitive, and the slug that names the account's storage.
// Slugs also remove accents and separators.
func NormalizeUsername(username string) (display, key, slug string, err error) {
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

// ValidatePassword checks the length rules for new passwords. bcrypt uses at
// most 72 bytes.
func ValidatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || len(password) > 72 {
		return fmt.Errorf("password must have at least 12 characters and at most 72 bytes")
	}
	return nil
}
