package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/database"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/share"
	"github.com/royvandam/pigeon/pkg/storage"
	"github.com/royvandam/pigeon/pkg/storage/local"
)

type fixture struct {
	*Service
	dir      string
	accounts *account.Store
	shares   *share.Store
	user     account.User
}

// newFixture serves one account with quotaBytes of quota, charging one byte
// per item so that quota arithmetic stays exact.
func newFixture(t *testing.T, quotaBytes int64) fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Open(ctx, filepath.Join(root, "pigeon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	accounts, err := account.NewStore(db, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u, err := accounts.Create(ctx, "Jane Doe", "test-password", false, quotaBytes)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := local.NewProvider(filepath.Join(root, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	shares := share.NewStore(db)
	s := NewService(provider, accounts, account.NewLocks(), quota.NewCache(time.Now), shares, 1)
	return fixture{Service: s, dir: provider.Directory(), accounts: accounts, shares: shares, user: u}
}

func (f fixture) write(t *testing.T, name, contents string) {
	t.Helper()
	filename := filepath.Join(f.dir, f.user.Slug, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o640); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) open(t *testing.T) *Access {
	t.Helper()
	a, err := f.Open(context.Background(), f.user)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func (f fixture) used(t *testing.T) int64 {
	t.Helper()
	used, err := f.Usage(context.Background(), f.user)
	if err != nil {
		t.Fatal(err)
	}
	return used
}

func TestOpenRejectsIdentitiesThatAreNoLongerCurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t, 100)
	a := f.open(t)
	if usage, err := a.Usage(ctx); err != nil || usage != (Usage{Quota: 100}) || usage.Remaining() != 100 || !usage.Known() {
		t.Fatalf("usage of a new account = %#v, %v", usage, err)
	}
	a.Close()
	a.Close() // Closing twice is harmless.
	if _, err := f.accounts.ResetPassword(ctx, f.user.ID, "replacement-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Open(ctx, f.user); !errors.Is(err, account.ErrInvalidCredentials) {
		t.Fatalf("an identity read before a password reset opened the account: %v", err)
	}
	if (Usage{}).Known() {
		t.Fatal("the zero usage is known")
	}
}

func TestFileActionsKeepUsageAndShareLinksUpToDate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t, 100)
	f.write(t, "a.txt", "12345")
	f.write(t, "folder/b.txt", "123")
	if used := f.used(t); used != 5+1+3 {
		t.Fatalf("measured usage = %d", used)
	}
	a := f.open(t)
	link, err := f.shares.Create(ctx, f.user, "folder/b.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	// A rename to the same name, and rejected changes, keep links.
	if err := a.Rename(ctx, "folder", "folder"); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func() error{
		"move into itself":     func() error { return a.Move(ctx, "folder", "folder") },
		"rename with a slash":  func() error { return a.Rename(ctx, "folder", "x/y") },
		"rename onto an item":  func() error { return a.Rename(ctx, "folder", "a.txt") },
		"move to a file":       func() error { return a.Move(ctx, "folder", "a.txt") },
		"invisible formatting": func() error { return a.Rename(ctx, "a.txt", "invoice‮fdp.exe") },
		"delete the top":       func() error { return a.Delete(ctx, "") },
		"reserved name":        func() error { return a.Mkdir(ctx, "", ".upload-x") },
	} {
		if err := change(); !errors.Is(err, storage.ErrInvalidPath) && !errors.Is(err, storage.ErrConflict) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.shares.Find(ctx, link); err != nil {
		t.Fatalf("a rejected change revoked a link: %v", err)
	}
	if err := a.Mkdir(ctx, "", "new"); err != nil {
		t.Fatal(err)
	}
	if err := a.Move(ctx, "folder", "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.shares.Find(ctx, link); !errors.Is(err, share.ErrNotFound) {
		t.Fatalf("moving a folder kept a link below it: %v", err)
	}
	if err := a.Rename(ctx, "new/folder", "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, "new"); err != nil {
		t.Fatal(err)
	}
	a.Close()
	// Only a.txt remains. The cache followed every change without measuring.
	if used, _, ok := f.usage.Cached(f.user.ID); !ok || used != 5 {
		t.Fatalf("cached usage = %d, %v; want 5", used, ok)
	}
	f.usage.Forget(f.user.ID)
	if used := f.used(t); used != 5 {
		t.Fatalf("measured usage = %d; want 5", used)
	}
}

func TestNewFoldersNeedQuota(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t, 2)
	f.write(t, "a.txt", "1")
	a := f.open(t)
	if err := a.Mkdir(ctx, "", "first"); err != nil {
		t.Fatal(err)
	}
	if err := a.Mkdir(ctx, "", "second"); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("a folder beyond the quota: %v", err)
	}
	if err := a.Mkdir(ctx, "a.txt", "inside"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("a folder inside a file: %v", err)
	}
}

func TestDownloadsStayBoundAndSharedLinksCheckTheirItem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t, 100)
	f.write(t, "project/a.txt", "a")
	f.write(t, "report‮txt.exe", "report")
	a := f.open(t)
	file, err := a.OpenDownload(ctx, "report‮txt.exe")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	folder, err := a.OpenDownload(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	top, err := a.OpenDownload(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	top.Close()
	a.Close() // Downloads stream after the account lock is released.
	if file.Folder() || file.Name() != "reporttxt.exe" || !folder.Folder() || folder.Name() != "project" || top.Name() != f.user.Slug {
		t.Fatalf("names: %q %q %q", file.Name(), folder.Name(), top.Name())
	}
	if data, err := io.ReadAll(file.File()); err != nil || string(data) != "report" {
		t.Fatalf("file contents: %q, %v", data, err)
	}
	var archive bytes.Buffer
	if err := folder.WriteZip(ctx, &archive); err != nil || !bytes.Contains(archive.Bytes(), []byte("project/a.txt")) {
		t.Fatalf("archive: %v", err)
	}

	secret, err := f.shares.Create(ctx, f.user, "project", true)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := f.OpenShared(ctx, secret)
	if err != nil || !shared.Folder() {
		t.Fatalf("shared folder: %v", err)
	}
	shared.Close()
	// A file now stands where the shared folder was.
	if err := os.RemoveAll(filepath.Join(f.dir, f.user.Slug, "project")); err != nil {
		t.Fatal(err)
	}
	f.write(t, "project", "not a folder")
	if _, err := f.OpenShared(ctx, secret); !errors.Is(err, share.ErrNotFound) {
		t.Fatalf("a link opened an item of another type: %v", err)
	}
}

// progress records what an upload reports.
type progress struct {
	preparing int
	written   int64
}

func (p *progress) Preparing()      { p.preparing++ }
func (p *progress) Written(n int64) { p.written += n }

func TestUploadsPublishTogetherWithinTheQuota(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t, 20)
	f.write(t, "existing.txt", "123")
	var p progress
	up, err := f.BeginUpload(ctx, f.user, &p)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	usage, err := up.Prepare(ctx, "")
	if err != nil || usage != (Usage{Quota: 20, Used: 3}) || p.preparing != 1 {
		t.Fatalf("Prepare = %#v, %v; reported preparing %d times", usage, err, p.preparing)
	}
	for name, contents := range map[string]string{"../escape": "", "existing.txt": "", ".upload-x/a": ""} {
		if err := up.Add(ctx, name, strings.NewReader(contents)); !errors.Is(err, storage.ErrInvalidPath) && !errors.Is(err, storage.ErrConflict) {
			t.Errorf("Add(%q) = %v", name, err)
		}
	}
	if err := up.Add(ctx, "folder/a.txt", strings.NewReader("12345")); err != nil {
		t.Fatal(err)
	}
	if err := up.Add(ctx, "folder/a.txt", strings.NewReader("again")); !errors.Is(err, storage.ErrInvalidPath) {
		t.Fatalf("a duplicate path: %v", err)
	}
	// The folder and both files use 1 + 5 + 10 of the 17 bytes left.
	var quotaErr *QuotaError
	if err := up.Add(ctx, "folder/big.txt", strings.NewReader(strings.Repeat("x", 12))); !errors.As(err, &quotaErr) || quotaErr.Available != 17 || !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("a file beyond the quota: %v", err)
	}
	if err := up.Add(ctx, "b.txt", strings.NewReader(strings.Repeat("x", 10))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, f.user.Slug, "folder")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("files appeared before publication: %v", err)
	}
	receipt, err := up.Publish(ctx)
	if err != nil || receipt.SavedAs != "." || !receipt.Group || receipt.Files != 2 || receipt.Bytes != 15 || receipt.Usage != (Usage{Quota: 20, Used: 19}) {
		t.Fatalf("Publish = %#v, %v", receipt, err)
	}
	// The rejected file was written as far as it fit: 11 bytes.
	if p.written != 5+11+10 {
		t.Fatalf("reported %d written bytes", p.written)
	}
	up.Close()
	if used := f.used(t); used != 19 {
		t.Fatalf("usage after publication = %d", used)
	}
}

func TestUploadsRecheckTheQuotaBeforePublishing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t, 20)
	up, err := f.BeginUpload(ctx, f.user, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	if _, err := up.Prepare(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := up.Add(ctx, "a.txt", strings.NewReader("1234567890")); err != nil {
		t.Fatal(err)
	}
	// The quota was lowered while the file streamed.
	if err := f.accounts.SetQuota(ctx, f.user.ID, 5); err != nil {
		t.Fatal(err)
	}
	var quotaErr *QuotaError
	receipt, err := up.Publish(ctx)
	if !errors.As(err, &quotaErr) || quotaErr.Available != 5 || receipt.Usage != (Usage{Quota: 5}) {
		t.Fatalf("Publish = %#v, %v", receipt, err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, f.user.Slug, "a.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a rejected upload was published: %v", err)
	}

	// Another upload waits for the slot, and an exhausted quota is reported
	// before any file is read.
	waiting, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := f.BeginUpload(waiting, f.user, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a second upload did not wait for the slot: %v", err)
	}
	up.Close()
	f.write(t, "full.txt", "12345")
	f.usage.Forget(f.user.ID)
	up, err = f.BeginUpload(ctx, f.user, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	if usage, err := up.Prepare(ctx, ""); !errors.As(err, &quotaErr) || usage != (Usage{Quota: 5, Used: 5}) {
		t.Fatalf("Prepare with an exhausted quota = %#v, %v", usage, err)
	}
}
