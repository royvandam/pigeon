package account

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/royvandam/pigeon/pkg/database"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/storage"
)

const testPassword = "test-password"

func newTestStore(t *testing.T, cost int) *Store {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "pigeon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := NewStore(db, cost)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func create(t *testing.T, s *Store, username string, admin bool) User {
	t.Helper()
	u, err := s.Create(context.Background(), username, testPassword, admin, quota.DefaultQuota)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUsernameNormalization(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ username, slug string }{
		{" Jane Doe ", "jane-doe"},
		{"Élise_Müller", "elise-muller"},
		{"ＪＡＮＥ", "jane"},
		{"../../Alice", "alice"},
		{"中文 用户", "中文-用户"},
		{"Straße", "strasse"},
	} {
		_, _, got, err := NormalizeUsername(tt.username)
		if err != nil || got != tt.slug {
			t.Errorf("NormalizeUsername(%q) = %q, %v; want %q", tt.username, got, err, tt.slug)
		}
	}
	for _, input := range []string{"", "...", "../", "user\x00name", "user‮name", strings.Repeat("x", 65)} {
		if _, _, _, err := NormalizeUsername(input); err == nil {
			t.Errorf("accepted invalid username %q", input)
		}
	}
}

func TestNewPasswordsNeedTwelveCharacters(t *testing.T) {
	t.Parallel()
	if err := ValidatePassword("elevenchars"); err == nil {
		t.Fatal("an 11-character password was accepted")
	}
	if err := ValidatePassword("twelve-chars"); err != nil {
		t.Fatalf("a 12-character password was rejected: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("é", 37)); err == nil {
		t.Fatal("a password longer than bcrypt's 72 bytes was accepted")
	}
}

func TestUsernameAndFolderCollisionsStayReserved(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, bcrypt.MinCost)
	ctx := context.Background()
	u := create(t, s, "Élise Smith", false)
	for _, name := range []string{"ÉLISE SMITH", "Elise_Smith"} {
		if _, err := s.Create(ctx, name, testPassword, false, quota.DefaultQuota); !errors.Is(err, ErrUserConflict) {
			t.Fatalf("create %q = %v; want collision", name, err)
		}
	}
	if err := s.Delete(ctx, u.ID, 999); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Elise Smith", testPassword, false, quota.DefaultQuota); !errors.Is(err, ErrUserConflict) {
		t.Fatalf("deleted folder was reused: %v", err)
	}
	if id, err := s.DeletedIDForUsername(ctx, "elise smith"); err != nil || id != u.ID {
		t.Fatalf("reserving account = %d, %v; want %d", id, err, u.ID)
	}
}

func TestUnknownUsernamesCheckAHashOfTheConfiguredCost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if DefaultPasswordCost != bcrypt.DefaultCost {
		t.Fatalf("production password cost = %d, want bcrypt's default %d", DefaultPasswordCost, bcrypt.DefaultCost)
	}
	for _, cost := range []int{bcrypt.MinCost, bcrypt.MinCost + 1} {
		s := newTestStore(t, cost)
		create(t, s, "Test User", false)
		var stored []byte
		if err := s.db.QueryRow(`SELECT password_hash FROM users`).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		// Equal costs make a wrong password and an unknown username take as long.
		storedCost, storedErr := bcrypt.Cost(stored)
		dummyCost, dummyErr := bcrypt.Cost(s.passwords.dummy)
		if storedErr != nil || dummyErr != nil || storedCost != cost || dummyCost != cost {
			t.Fatalf("cost %d: stored hash cost %d (%v), dummy hash cost %d (%v)", cost, storedCost, storedErr, dummyCost, dummyErr)
		}
		if _, err := s.Authenticate(ctx, "Nobody", testPassword); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("unknown username authenticated: %v", err)
		}
		if _, err := s.Authenticate(ctx, "Test User", testPassword); err != nil {
			t.Fatalf("known username: %v", err)
		}
	}
}

func TestQuotaChecksTheIdentityThatWasRead(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, bcrypt.MinCost)
	ctx := context.Background()
	u := create(t, s, "Test User", false)
	if bytes, err := s.Quota(ctx, u); err != nil || bytes != quota.DefaultQuota {
		t.Fatalf("quota = %d, %v", bytes, err)
	}
	if err := s.SetQuota(ctx, u.ID, 12345); err != nil {
		t.Fatal(err)
	}
	if bytes, err := s.Quota(ctx, u); err != nil || bytes != 12345 {
		t.Fatalf("changed quota = %d, %v", bytes, err)
	}
	if _, err := s.ResetPassword(ctx, u.ID, "replacement-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Quota(ctx, u); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("an identity read before a password reset is still current: %v", err)
	}
}

func TestLocksSerializeEachAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := NewLocks()
	unlock, err := l.Lock(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := l.Lock(canceled, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request waited for the account lock: %v", err)
	}
	other, err := l.Lock(ctx, 2)
	if err != nil {
		t.Fatalf("another account waited: %v", err)
	}
	other()
	// An upload streaming without the account lock still holds off removal.
	unlock()
	releaseSlot, err := l.LockUploadSlot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := l.LockForRemoval(waiting, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("removal did not wait for the upload slot: %v", err)
	}
	releaseSlot()
	release, err := l.LockForRemoval(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Lock(canceled, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("removal did not take the account lock")
	}
	release()
	if unlock, err := l.Lock(ctx, 1); err != nil {
		t.Fatalf("removal did not release the account lock: %v", err)
	} else {
		unlock()
	}
}

func TestManagerRequiresAnAdministrator(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, bcrypt.MinCost)
	ctx := context.Background()
	u := create(t, s, "Test User", false)
	m := NewManager(s, NewLocks(), quota.NewCache(time.Now), nil)
	for name, change := range map[string]func() error{
		"create": func() error {
			_, err := m.Create(ctx, u, "Unauthorized", true, quota.DefaultQuota)
			return err
		},
		"recycle": func() error { _, err := m.Recycle(ctx, u, u.ID); return err },
		"reset":   func() error { _, err := m.ResetPassword(ctx, u, u.ID); return err },
		"delete":  func() error { return m.Delete(ctx, u, u.ID) },
		"quota":   func() error { return m.SetQuota(ctx, u, u.ID, 1) },
		"purge":   func() error { return m.Purge(ctx, u, u.ID) },
	} {
		if err := change(); !errors.Is(err, ErrAdministratorRequired) {
			t.Errorf("a regular user could %s: %v", name, err)
		}
	}
	if users, err := s.List(ctx); err != nil || len(users) != 1 || users[0].QuotaBytes != quota.DefaultQuota {
		t.Fatalf("rejected changes modified accounts: %#v, %v", users, err)
	}
	if _, err := s.Authenticate(ctx, u.Username, testPassword); err != nil {
		t.Fatalf("rejected changes modified the password: %v", err)
	}
}

// removals records the namespaces a provider was asked to remove, and fails
// while err is set.
type removals struct {
	storage.Provider
	removed []string
	err     error
}

func (r *removals) Remove(ctx context.Context, namespace string) error {
	r.removed = append(r.removed, namespace)
	return r.err
}

func TestPurgeRemovesFilesBeforeReleasingTheUsername(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, bcrypt.MinCost)
	ctx := context.Background()
	admin := create(t, s, "Admin", true)
	u := create(t, s, "Retired User", false)
	usage := quota.NewCache(time.Now)
	provider := &removals{err: errors.New("storage unavailable")}
	m := NewManager(s, NewLocks(), usage, provider)
	if err := m.Purge(ctx, admin, u.ID); !errors.Is(err, ErrDeletedUserNotFound) || len(provider.removed) != 0 {
		t.Fatalf("purged an active account: %v, removed %q", err, provider.removed)
	}
	if err := m.Delete(ctx, admin, u.ID); err != nil {
		t.Fatal(err)
	}
	measure := func(context.Context) (int64, error) { return 10, nil }
	usage.Usage(ctx, u.ID, measure)
	if err := m.Purge(ctx, admin, u.ID); !errors.Is(err, provider.err) {
		t.Fatalf("purge ignored a storage failure: %v", err)
	}
	if _, _, ok := usage.Cached(u.ID); ok {
		t.Fatal("a failed removal kept the cached usage, though it may have removed files")
	}
	if _, err := s.Create(ctx, u.Username, testPassword, false, quota.DefaultQuota); !errors.Is(err, ErrUserConflict) {
		t.Fatalf("a failed purge released the username: %v", err)
	}
	provider.err = nil
	if err := m.Purge(ctx, admin, u.ID); err != nil {
		t.Fatal(err)
	}
	if len(provider.removed) != 2 || provider.removed[0] != u.Slug || provider.removed[1] != u.Slug {
		t.Fatalf("removed namespaces %q; want %q twice", provider.removed, u.Slug)
	}
	if fresh, err := s.Create(ctx, u.Username, testPassword, false, quota.DefaultQuota); err != nil || fresh.ID == u.ID {
		t.Fatalf("purged username or ID was not released safely: %#v, %v", fresh, err)
	}
}

func TestManagerChangesWaitForTheAccountLock(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, bcrypt.MinCost)
	ctx := context.Background()
	admin := create(t, s, "Admin", true)
	u := create(t, s, "Test User", false)
	locks := NewLocks()
	m := NewManager(s, locks, quota.NewCache(time.Now), &removals{})
	unlock, err := locks.Lock(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	// Already canceled contexts would also fail in the database, so only waits
	// that time out show that a change waits for the lock.
	wait := func() context.Context {
		waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		t.Cleanup(cancel)
		return waitCtx
	}
	if err := m.SetQuota(wait(), admin, u.ID, 12345); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a quota change did not wait: %v", err)
	}
	if err := s.Delete(ctx, u.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Recycle(wait(), admin, u.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recycling did not wait: %v", err)
	}
	if err := m.Purge(wait(), admin, u.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("purging did not wait: %v", err)
	}
}
