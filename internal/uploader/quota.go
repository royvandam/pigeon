package uploader

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
)

const defaultUserQuota = int64(1 << 30)

func validateQuota(quota int64) error {
	if quota <= 0 || quota > math.MaxInt64-multipartOverhead {
		return fmt.Errorf("quota must be positive and allow for multipart overhead")
	}
	return nil
}

func parseQuota(value string) (int64, error) {
	quota, err := parseSize(value)
	if err != nil {
		return 0, err
	}
	return quota, validateQuota(quota)
}

// Serialize account storage access and lifecycle changes. Uploads hold this lock
// from the quota check through publication; reads recheck identity while holding
// it. Different accounts can proceed concurrently.
func (s *store) lockUploads(ctx context.Context, id int64) (func(), error) {
	value, _ := s.uploadLocks.LoadOrStore(id, make(chan struct{}, 1))
	lock := value.(chan struct{})
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *store) userQuota(ctx context.Context, u user) (int64, error) {
	var quota int64
	err := s.db.QueryRowContext(ctx, `SELECT quota_bytes FROM users WHERE id = ? AND auth_version = ? AND deleted_at IS NULL`, u.ID, u.Version).Scan(&quota)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errInvalidCredentials
	}
	if err != nil {
		return 0, err
	}
	return quota, validateQuota(quota)
}

func (s *store) setQuota(ctx context.Context, id, quota int64) error {
	if err := validateQuota(quota); err != nil {
		return err
	}
	unlock, err := s.lockUploads(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE users SET quota_bytes = ? WHERE id = ? AND deleted_at IS NULL`, quota, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errUserNotFound
	}
	return nil
}

func remainingQuota(quota, used int64) int64 {
	if used >= quota {
		return 0
	}
	return quota - used
}

func quotaInput(quota int64) string {
	for _, unit := range []struct {
		bytes int64
		name  string
	}{{1 << 40, "TB"}, {1 << 30, "GB"}, {1 << 20, "MB"}, {1 << 10, "KB"}, {1, "B"}} {
		if quota%unit.bytes == 0 {
			return strconv.FormatInt(quota/unit.bytes, 10) + unit.name
		}
	}
	return ""
}

func setQuotaHeaders(w http.ResponseWriter, quota, used int64) {
	w.Header().Set("X-Quota-Limit", strconv.FormatInt(quota, 10))
	w.Header().Set("X-Quota-Used", strconv.FormatInt(used, 10))
	w.Header().Set("X-Quota-Remaining", strconv.FormatInt(remainingQuota(quota, used), 10))
}
