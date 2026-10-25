package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/share"
	"github.com/royvandam/pigeon/pkg/storage"
)

// QuotaError reports files that do not fit the account's quota. It wraps
// quota.ErrExceeded.
type QuotaError struct {
	// Available is how much of the quota the upload could use.
	Available int64
}

func (e *QuotaError) Error() string {
	return quota.ErrExceeded.Error()
}

func (e *QuotaError) Unwrap() error {
	return quota.ErrExceeded
}

// Receipt describes a published upload.
type Receipt struct {
	// SavedAs is the path of a single file, or the destination folder of a
	// group, "." for the top folder. For an upload through a link, it is
	// relative to the link's folder.
	SavedAs string
	// Group reports several files, or files in folders.
	Group bool
	Files int
	Bytes int64
	// Usage is the account's usage once Publish returns, when known.
	Usage Usage
}

// Progress follows an upload. Its methods are called from the goroutine that
// drives the upload.
type Progress interface {
	// Preparing is called once the upload has the account to itself and starts
	// checking its storage, which can take a while the first time.
	Preparing()
	// Written is called with the number of file bytes after each write to
	// storage.
	Written(n int64)
}

// Upload receives files for one account and publishes them together, so a
// batch appears all at once or not at all; existing items are never
// replaced.
//
// An upload holds the account's upload slot from BeginUpload until Close, so
// uploads to one account stream one at a time and each starts from the quota
// the previous one left. It holds the account lock only in Prepare and
// Publish, so the account's pages, share links, and file actions do not wait
// while files stream.
type Upload struct {
	service     *Service
	user        account.User
	progress    Progress
	releaseSlot func()
	tree        storage.Tree
	stage       storage.Upload
	target      string
	// secret and link are the token and link of an upload through an upload
	// link, which renames items whose names are taken; see freeName.
	secret    string
	link      share.Link
	renamed   map[string]string // first path element → its free name
	taken     map[string]bool   // free names chosen so far
	remaining int64             // quota available before this batch
	paths     map[string]bool   // true for files, false for their parent folders
	names     []string
	firstPath string
	files     int
	bytes     int64 // file contents received
	charged   int64 // quota charge for those files and the folders they add
	folders   int64 // folders included in charged
}

// BeginUpload waits until no other upload to u's account is streaming, and
// starts an upload into folder dir. progress, if not nil, follows the upload.
// Close the upload when done with it.
func (s *Service) BeginUpload(ctx context.Context, u account.User, dir string, progress Progress) (*Upload, error) {
	release, err := s.locks.LockUploadSlot(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &Upload{service: s, user: u, progress: progress, releaseSlot: release, target: dir, paths: make(map[string]bool)}, nil
}

// BeginSharedUpload starts an upload through the upload link with token
// secret, into the link's folder and charged to its owner, like BeginUpload.
// Files and folders whose names are taken in the folder are renamed, so an
// upload through a link never adds to or replaces what the folder holds. Its
// error wraps share.ErrNotFound when the link is gone or is a download link;
// Prepare and Publish check the link again.
func (s *Service) BeginSharedUpload(ctx context.Context, secret string, progress Progress) (*Upload, error) {
	link, err := s.UploadLink(ctx, secret)
	if err != nil {
		return nil, err
	}
	up, err := s.BeginUpload(ctx, link.User, link.Path, progress)
	if err != nil {
		return nil, err
	}
	up.secret, up.link, up.renamed, up.taken = secret, link, make(map[string]string), make(map[string]bool)
	return up, nil
}

// open takes the account lock for a step of the upload. An upload through a
// link checks that the link still points to its folder, before and again
// after waiting for the lock, and acts for the account as it is now, so a
// password change while files stream does not stop it.
func (up *Upload) open(ctx context.Context) (*Access, error) {
	if up.secret == "" {
		return up.service.Open(ctx, up.user)
	}
	link, err := up.service.currentLink(ctx, up.secret, up.link)
	if err != nil {
		return nil, err
	}
	up.user = link.User
	access, err := up.service.Open(ctx, up.user)
	if err != nil {
		return nil, err
	}
	if _, err := up.service.currentLink(ctx, up.secret, up.link); err != nil {
		access.Close()
		return nil, err
	}
	return access, nil
}

// Prepare checks the account and that the destination folder has room for at
// least one more item, and starts staging files for it. It returns the
// account's usage when known, also when the quota is exhausted, which a
// *QuotaError reports.
func (up *Upload) Prepare(ctx context.Context) (Usage, error) {
	access, err := up.open(ctx)
	if err != nil {
		return Usage{}, err
	}
	defer access.Close()
	if up.progress != nil {
		up.progress.Preparing()
	}
	// The upload keeps its own handle, which it needs after the lock is
	// released.
	tree, err := up.service.storage.Create(ctx, up.user.Slug)
	if err != nil {
		return Usage{}, err
	}
	usage, err := access.Usage(ctx)
	if err == nil && usage.Remaining() < up.service.unit {
		err = &QuotaError{Available: usage.Remaining()}
	}
	if err == nil {
		err = storage.RequireDirectory(ctx, tree, up.target)
	}
	var stage storage.Upload
	if err == nil {
		stage, err = tree.BeginUpload(ctx)
	}
	if err != nil {
		tree.Close()
		return usage, err
	}
	up.tree, up.stage, up.remaining = tree, stage, usage.Remaining()
	return usage, nil
}

// Files returns how many files were added.
func (up *Upload) Files() int {
	return up.files
}

func (up *Upload) checkPath(ctx context.Context, name string) error {
	if err := storage.ValidatePath(name); err != nil {
		return err
	}
	// Only the uploaded path is new; the destination folder may already exist
	// with a name this rule would reject.
	if err := validateNewName(name); err != nil {
		return err
	}
	if _, exists := up.paths[name]; exists {
		return fmt.Errorf("%w: duplicate or conflicting file path", storage.ErrInvalidPath)
	}
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if up.paths[parent] {
			return fmt.Errorf("%w: file path conflicts with a parent file", storage.ErrInvalidPath)
		}
	}
	destination := path.Join(up.target, name)
	if info, err := up.tree.Stat(ctx, destination); err == nil && info != nil {
		return fmt.Errorf("%w: %q; rename or delete the existing item first", storage.ErrConflict, destination)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// newDirectories counts the folders that publishing name would create: parents
// that neither exist in storage nor belong to an earlier file in this batch.
func (up *Upload) newDirectories(ctx context.Context, name string) (int64, error) {
	var count int64
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if _, planned := up.paths[parent]; planned {
			break
		}
		if _, err := up.tree.Stat(ctx, path.Join(up.target, parent)); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
		count++
	}
	return count, nil
}

// Add stages a file at name, relative to the destination folder, reading its
// contents. It rejects invalid, duplicate, and taken paths before reading,
// with errors that wrap storage.ErrInvalidPath or storage.ErrConflict, and
// reports a *QuotaError as soon as the batch outgrows the quota. An upload
// through a link renames taken names instead.
func (up *Upload) Add(ctx context.Context, name string, contents io.Reader) error {
	if up.secret != "" {
		var err error
		if name, err = up.freeName(ctx, name); err != nil {
			return err
		}
	}
	if err := up.checkPath(ctx, name); err != nil {
		return err
	}
	folders, err := up.newDirectories(ctx, name)
	if err != nil {
		return err
	}
	unit := up.service.unit
	// Even an empty file needs one unit. File contents may use whole units only.
	available := up.remaining - up.charged - folders*unit
	if available < unit {
		return &QuotaError{Available: up.remaining}
	}
	out, err := up.stage.Create(name)
	if err != nil {
		return err
	}
	// Never write the byte used to detect an overflow, even when earlier files
	// in this batch have consumed all of the remaining quota.
	limited := &io.LimitedReader{R: contents, N: available / unit * unit}
	n, copyErr := io.Copy(progressWriter{Writer: out, progress: up.progress}, limited)
	if copyErr == nil && limited.N == 0 {
		var extra [1]byte
		_, probeErr := io.ReadFull(contents, extra[:])
		if probeErr == nil {
			copyErr = &QuotaError{Available: up.remaining}
		} else if probeErr != io.EOF {
			copyErr = probeErr
		}
	}
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if up.files == 0 {
		up.firstPath = name
	}
	up.paths[name] = true
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		up.paths[parent] = false
	}
	up.files++
	up.bytes += n
	up.charged += folders*unit + quota.Charge(n, unit)
	up.folders += folders
	up.names = append(up.names, name)
	return nil
}

// currentCharge recomputes the batch's charge against the destination as it is
// now. Files stream without the account lock, so folders may have been created
// or removed since they were added.
func (up *Upload) currentCharge(ctx context.Context) (int64, error) {
	planned := map[string]bool{}
	var folders int64
	for _, name := range up.names {
		for parent := path.Dir(name); parent != "." && !planned[parent]; parent = path.Dir(parent) {
			planned[parent] = true
			if _, err := up.tree.Stat(ctx, path.Join(up.target, parent)); err == nil {
				break
			} else if !errors.Is(err, fs.ErrNotExist) {
				return 0, err
			}
			folders++
		}
	}
	return up.charged + (folders-up.folders)*up.service.unit, nil
}

// Publish checks the account and its quota again and publishes the files.
// The account may have been deleted, its quota lowered, or its storage filled
// by file actions while the files streamed. An upload through a link also
// checks that the names it chose are still free; see checkFreeNames.
func (up *Upload) Publish(ctx context.Context) (Receipt, error) {
	access, err := up.open(ctx)
	if err != nil {
		return Receipt{}, err
	}
	defer access.Close()
	if err := up.checkFreeNames(ctx); err != nil {
		return Receipt{}, err
	}
	usage, err := access.Usage(ctx) // Staged files are never counted.
	if err != nil {
		return Receipt{}, err
	}
	charged, err := up.currentCharge(ctx)
	if err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{Usage: usage}
	if usage.Remaining() < charged {
		return receipt, &QuotaError{Available: usage.Remaining()}
	}
	// File actions take the same account lock, so nothing changes the
	// destination meanwhile.
	if err := up.stage.Publish(ctx, up.target, up.names); err != nil {
		access.forgetUsage() // Publication rolls back, but only as far as it can.
		return receipt, err
	}
	access.changed(charged)
	receipt.Usage.Used += charged
	receipt.Files, receipt.Bytes = up.files, up.bytes
	receipt.Group = up.files > 1 || path.Dir(up.firstPath) != "."
	folder := up.target
	if up.secret != "" {
		folder = "" // Someone uploading through a link sees only the names in its folder.
	}
	receipt.SavedAs = path.Join(folder, up.firstPath)
	if receipt.Group {
		receipt.SavedAs = folder
		if receipt.SavedAs == "" {
			receipt.SavedAs = "."
		}
	}
	return receipt, nil
}

// maxRenames limits the numbered names an upload through a link tries for one
// item.
const maxRenames = 1000

// freeName returns name with its first element replaced, if that is taken in
// the destination folder, by the first free numbered name, such as
// "report (2).pdf" or "Photos (2)". Every file under one first element gets the
// same name, so an uploaded folder stays together, and an upload through a
// link never adds files to a folder that was already there.
func (up *Upload) freeName(ctx context.Context, name string) (string, error) {
	if err := storage.ValidatePath(name); err != nil {
		return "", err
	}
	first, rest, nested := strings.Cut(name, "/")
	free, ok := up.renamed[first]
	if !ok {
		var err error
		if free, err = up.freeFirstElement(ctx, first, nested); err != nil {
			return "", err
		}
		up.renamed[first], up.taken[free] = free, true
	}
	if nested {
		return free + "/" + rest, nil
	}
	return free, nil
}

// checkFreeNames reports a storage.ErrConflict if an item has taken one of the
// names that an upload through a link chose as free. Files stream without the
// account lock, so the owner may have created one since; publishing would then
// add files to an existing folder. Publish calls it with the lock held, so the
// names stay free until the files are published, and trying again chooses new
// ones.
func (up *Upload) checkFreeNames(ctx context.Context) error {
	for _, free := range up.renamed {
		_, err := up.tree.Stat(ctx, path.Join(up.target, free))
		if err == nil {
			return fmt.Errorf("%w: a name was taken during the upload; try again", storage.ErrConflict)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (up *Upload) freeFirstElement(ctx context.Context, first string, folder bool) (string, error) {
	for n := 1; n <= maxRenames; n++ {
		candidate := numberedName(first, n, folder)
		if up.taken[candidate] {
			continue
		}
		_, err := up.tree.Stat(ctx, path.Join(up.target, candidate))
		if errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("%w: %q and its numbered names are taken", storage.ErrConflict, first)
}

// numberedName returns name for n = 1, and otherwise name with " (n)" before
// a file's extension, shortening the name if needed to keep it a valid length.
func numberedName(name string, n int, folder bool) string {
	if n == 1 {
		return name
	}
	base, ext := name, ""
	if e := path.Ext(name); !folder && e != name {
		base, ext = strings.TrimSuffix(name, e), e
	}
	suffix := fmt.Sprintf(" (%d)", n)
	keep := storage.MaxNameBytes - len(suffix) - len(ext)
	if keep < 1 {
		base, ext, keep = name, "", storage.MaxNameBytes-len(suffix)
	}
	if len(base) > keep {
		for keep > 0 && !utf8.RuneStart(base[keep]) {
			keep--
		}
		base = base[:keep]
	}
	return base + suffix + ext
}

// Close discards every file that was not published and releases the upload
// slot. An error means some staged files remain until the next start removes
// them.
func (up *Upload) Close() error {
	var err error
	if up.stage != nil {
		err = up.stage.Close()
		up.stage = nil
	}
	if up.tree != nil {
		up.tree.Close()
		up.tree = nil
	}
	if up.releaseSlot != nil {
		up.releaseSlot()
		up.releaseSlot = nil
	}
	return err
}

// progressWriter reports file bytes only after the output accepts them.
// Multipart framing and bytes still buffered in the browser or a proxy do not
// count.
type progressWriter struct {
	io.Writer
	progress Progress
}

func (w progressWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if w.progress != nil {
		w.progress.Written(int64(n))
	}
	return n, err
}
