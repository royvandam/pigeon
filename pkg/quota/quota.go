// Package quota limits how much storage each account may use.
//
// Every file and folder is charged at least one allocation unit, and file
// sizes are rounded up to whole units, so a quota bounds disk allocation and
// the number of items rather than only file contents. Measure computes an
// account's usage from its files, and Cache keeps the result up to date so
// requests need not walk the account's files every time.
package quota

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"

	"github.com/royvandam/pigeon/pkg/storage"
)

const (
	// DefaultQuota is the quota of a new account unless another is chosen.
	DefaultQuota = int64(1 << 30)
	// DefaultAllocationUnit matches common filesystem block sizes.
	DefaultAllocationUnit = int64(4 << 10)
	// Headroom is how far every quota stays below the largest int64, so that
	// request framing, such as an upload's multipart headers, can be allowed
	// for on top of a remaining quota without overflowing.
	Headroom = int64(10 << 20)
)

// ErrExceeded reports a change that does not fit an account's quota.
var ErrExceeded = errors.New("disk quota exceeded")

// Validate checks that quota is positive and leaves Headroom.
func Validate(quota int64) error {
	if quota <= 0 || quota > math.MaxInt64-Headroom {
		return fmt.Errorf("quota must be positive and allow for multipart overhead")
	}
	return nil
}

// Remaining returns how much of quota is left after used, or zero.
func Remaining(quota, used int64) int64 {
	if used >= quota {
		return 0
	}
	return quota - used
}

// Charge returns the quota charge for a file of size bytes, at least one unit.
func Charge(size, unit int64) int64 {
	if size <= 0 {
		return unit
	}
	units := (size-1)/unit + 1
	if units > math.MaxInt64/unit {
		return math.MaxInt64
	}
	return units * unit
}

// Measure returns the quota charge of name and everything below it in tree.
// The top folder is not charged, because it is the account itself. Providers
// leave out uploads in progress: an upload is charged when it is published.
func Measure(ctx context.Context, tree storage.Tree, name string, unit int64) (int64, error) {
	var used int64
	err := tree.Walk(ctx, name, func(path string, info fs.FileInfo) error {
		charge := unit
		if info.IsDir() {
			if path == "" {
				return nil
			}
		} else {
			charge = Charge(info.Size(), unit)
		}
		if charge > math.MaxInt64-used {
			return fmt.Errorf("stored file sizes exceed the maximum supported usage")
		}
		used += charge
		return nil
	})
	return used, err
}
