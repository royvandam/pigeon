package pigeon

import "context"

// accountAccess holds one account's storage lock and current quota. Reads and
// writes recheck the authenticated identity after waiting, so purging and reusing
// a username cannot expose the replacement account to an older request.
type accountAccess struct {
	Files      userStorage
	QuotaBytes int64
	release    func()
}

func (cfg config) accessAccount(ctx context.Context, u user) (*accountAccess, error) {
	release, err := cfg.store.lockUploads(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	quota, err := cfg.store.userQuota(ctx, u)
	if err != nil {
		release()
		return nil, err
	}
	return &accountAccess{Files: cfg.filesFor(u), QuotaBytes: quota, release: release}, nil
}

// requireQuota reports errDiskQuotaExceeded unless n more bytes fit the quota.
func (access *accountAccess) requireQuota(ctx context.Context, n int64) error {
	used, err := access.Files.usage(ctx)
	if err != nil {
		return err
	}
	if remainingQuota(access.QuotaBytes, used) < n {
		return errDiskQuotaExceeded
	}
	return nil
}

func (access *accountAccess) close() {
	if access.release != nil {
		access.release()
		access.release = nil
	}
}
