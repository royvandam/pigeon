package pigeon

import (
	"context"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/quota"
)

// accountAccess holds one account's storage lock and current quota. Reads and
// writes recheck the authenticated identity after waiting, so purging and reusing
// a username cannot expose the replacement account to an older request.
type accountAccess struct {
	Files      userStorage
	QuotaBytes int64
	id         int64
	cache      *quota.Cache
	release    func()
}

func (cfg config) accessAccount(ctx context.Context, u account.User) (*accountAccess, error) {
	release, err := cfg.locks.Lock(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	limit, err := cfg.store.accounts.Quota(ctx, u)
	if err != nil {
		release()
		return nil, err
	}
	return &accountAccess{Files: cfg.filesFor(u), QuotaBytes: limit, id: u.ID, cache: cfg.usage, release: release}, nil
}

// used returns the account's storage usage, measuring its folder only when no
// value is cached.
func (access *accountAccess) used(ctx context.Context) (int64, error) {
	return access.cache.Usage(ctx, access.id, access.Files.usage)
}

// changed records delta bytes of usage added, or removed when negative, by a
// change made while holding this access.
func (access *accountAccess) changed(delta int64) {
	access.cache.Add(access.id, delta)
}

// forgetUsage drops the cached usage after a change that may have partly
// failed, so the next request measures the folder again.
func (access *accountAccess) forgetUsage() {
	access.cache.Forget(access.id)
}

// requireQuota reports quota.ErrExceeded unless n more bytes fit the quota.
func (access *accountAccess) requireQuota(ctx context.Context, n int64) error {
	used, err := access.used(ctx)
	if err != nil {
		return err
	}
	if quota.Remaining(access.QuotaBytes, used) < n {
		return quota.ErrExceeded
	}
	return nil
}

func (access *accountAccess) close() {
	if access.release != nil {
		access.release()
		access.release = nil
	}
}
