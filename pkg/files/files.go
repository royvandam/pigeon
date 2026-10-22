// Package files reads and changes the files of Pigeon's accounts: listing,
// creating folders, renaming, moving, deleting, downloading, and uploading.
//
// Every operation runs under the account's lock and checks that the identity
// it acts for is still current, so a request authenticated before a deletion,
// password change, or purge cannot reach the account's files, or those of a
// new account that reuses the username. Changes keep the account's cached
// usage up to date and revoke share links whose paths they change.
package files

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/share"
	"github.com/royvandam/pigeon/pkg/storage"
)

var (
	// ErrQuotaUnavailable reports that an account's quota could not be read.
	ErrQuotaUnavailable = errors.New("failed to load disk quota")
	// ErrUsageUnavailable reports that an account's usage could not be
	// measured.
	ErrUsageUnavailable = errors.New("failed to check storage usage")
)

// Usage is an account's quota and how much of it is used. Quotas are always
// positive, so the zero Usage means the usage is not known.
type Usage struct {
	Quota int64
	Used  int64
}

// Remaining returns how much of the quota is left.
func (u Usage) Remaining() int64 {
	return quota.Remaining(u.Quota, u.Used)
}

// Known reports whether u holds a measured usage.
func (u Usage) Known() bool {
	return u.Quota > 0
}

// Service gives access to the files of every account.
type Service struct {
	storage  storage.Provider
	accounts *account.Store
	locks    *account.Locks
	usage    *quota.Cache
	shares   *share.Store
	unit     int64
}

// NewService serves the files that provider stores for the accounts in
// accounts. It shares locks and usage with the account manager, and revokes
// links in shares when paths change. unit is the minimum quota charge per
// file and folder, normally quota.DefaultAllocationUnit.
func NewService(provider storage.Provider, accounts *account.Store, locks *account.Locks, usage *quota.Cache, shares *share.Store, unit int64) *Service {
	return &Service{storage: provider, accounts: accounts, locks: locks, usage: usage, shares: shares, unit: unit}
}

// AllocationUnit returns the minimum quota charge per file and folder.
func (s *Service) AllocationUnit() int64 {
	return s.unit
}

// measure returns a function that measures the files in namespace, as
// quota.Cache needs on a miss.
func (s *Service) measure(namespace string) func(context.Context) (int64, error) {
	return func(ctx context.Context) (int64, error) {
		tree, err := s.storage.Open(ctx, namespace)
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		defer tree.Close()
		return quota.Measure(ctx, tree, "", s.unit)
	}
}

// Open takes u's account lock and checks that u is still current, then gives
// access to the account's files. Its error wraps account.ErrInvalidCredentials
// when u is no longer current. Close the access to release the lock.
func (s *Service) Open(ctx context.Context, u account.User) (*Access, error) {
	release, err := s.locks.Lock(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	limit, err := s.accounts.Quota(ctx, u)
	if err != nil {
		release()
		if !errors.Is(err, account.ErrInvalidCredentials) {
			err = fmt.Errorf("%w: %w", ErrQuotaUnavailable, err)
		}
		return nil, err
	}
	return &Access{service: s, user: u, quota: limit, release: release}, nil
}

// Usage returns an account's storage usage for display, including deleted
// accounts, whose files are kept. It takes the account lock, like every other
// use of the usage cache, but skips the identity check.
func (s *Service) Usage(ctx context.Context, u account.User) (int64, error) {
	release, err := s.locks.Lock(ctx, u.ID)
	if err != nil {
		return 0, err
	}
	defer release()
	return s.usage.Usage(ctx, u.ID, s.measure(u.Slug))
}
