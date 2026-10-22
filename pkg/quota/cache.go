package quota

import (
	"context"
	"sync"
	"time"
)

// RemeasureAfter bounds how long files changed directly in storage, outside
// Pigeon, take to show in an account's usage.
const RemeasureAfter = 10 * time.Minute

// Cache remembers each account's storage usage, so requests do not walk the
// account's files every time. Callers hold the account lock while they read,
// fill, or change an account's entry; every change to an account's files holds
// that lock too, so an entry matches the files as changed through Pigeon.
// Entries are measured again after RemeasureAfter.
type Cache struct {
	mu      sync.Mutex
	entries map[int64]entry
	now     func() time.Time
}

type entry struct {
	used     int64
	measured time.Time
}

// NewCache returns an empty cache that reads the time from clock, such as
// time.Now.
func NewCache(clock func() time.Time) *Cache {
	return &Cache{entries: make(map[int64]entry), now: clock}
}

// Usage returns the cached usage of account id, or calls measure and caches the
// result.
func (c *Cache) Usage(ctx context.Context, id int64, measure func(context.Context) (int64, error)) (int64, error) {
	c.mu.Lock()
	e, ok := c.entries[id]
	now := c.now()
	c.mu.Unlock()
	if ok && now.Sub(e.measured) < RemeasureAfter {
		return e.used, nil
	}
	used, err := measure(ctx)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.entries[id] = entry{used: used, measured: now}
	c.mu.Unlock()
	return used, nil
}

// Cached returns the usage cached for account id and when it was measured,
// without measuring. ok is false when nothing is cached.
func (c *Cache) Cached(id int64) (used int64, measured time.Time, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	return e.used, e.measured, ok
}

// Add adjusts a cached value by delta; an account that was never measured stays
// unmeasured.
func (c *Cache) Add(id, delta int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[id]; ok {
		e.used = max(0, e.used+delta)
		c.entries[id] = e
	}
}

// Forget drops a cached value, so the next request measures the account again.
func (c *Cache) Forget(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, id)
}
