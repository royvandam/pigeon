package account

import (
	"context"
	"sync"
)

// Locks serializes the work done on each account. Different accounts proceed
// concurrently. Every lock waits until it is free or the context ends.
type Locks struct {
	accounts sync.Map
	uploads  sync.Map
}

// NewLocks returns a set of unlocked account locks.
func NewLocks() *Locks {
	return &Locks{}
}

// Lock takes account id's lock. Every read and change of the account's files
// holds it, as do quota changes, recycling, and purging. Uploads hold it to
// check the quota and again to publish, but not while streaming; reads recheck
// the account's identity while holding it. The returned function releases the
// lock.
func (l *Locks) Lock(ctx context.Context, id int64) (func(), error) {
	return lock(ctx, &l.accounts, id)
}

// LockUploadSlot takes account id's upload slot, which lets one upload per
// account stream at a time, so each starts from the quota the previous one
// left. Take it before Lock.
func (l *Locks) LockUploadSlot(ctx context.Context, id int64) (func(), error) {
	return lock(ctx, &l.uploads, id)
}

// LockForRemoval waits for a streaming upload as well as for the account lock,
// so purging or recycling an account never races with an upload's staging.
func (l *Locks) LockForRemoval(ctx context.Context, id int64) (func(), error) {
	releaseSlot, err := l.LockUploadSlot(ctx, id)
	if err != nil {
		return nil, err
	}
	release, err := l.Lock(ctx, id)
	if err != nil {
		releaseSlot()
		return nil, err
	}
	return func() { release(); releaseSlot() }, nil
}

func lock(ctx context.Context, locks *sync.Map, id int64) (func(), error) {
	value, _ := locks.LoadOrStore(id, make(chan struct{}, 1))
	lock := value.(chan struct{})
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
