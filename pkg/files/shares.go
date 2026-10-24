package files

import (
	"context"
	"fmt"
	"io/fs"
	"time"

	"github.com/royvandam/pigeon/pkg/share"
	"github.com/royvandam/pigeon/pkg/storage"
)

// Shared describes the link of the given kind for the item at name.
func (a *Access) Shared(ctx context.Context, name string, kind share.Kind) (share.Status, error) {
	if _, err := a.statItem(ctx, name); err != nil {
		return share.Status{}, err
	}
	return a.service.shares.Status(ctx, a.user, name, kind)
}

// Share creates a link of the given kind for the item at name, replacing any
// link of that kind it had, and returns the link's token and when it expires.
// A lifetime of zero creates a link that does not expire. Upload links need a
// folder; errors about the link wrap share.ErrInvalidLink.
func (a *Access) Share(ctx context.Context, name string, kind share.Kind, lifetime time.Duration) (string, time.Time, error) {
	info, err := a.statItem(ctx, name)
	if err != nil {
		return "", time.Time{}, err
	}
	return a.service.shares.Create(ctx, a.user, name, kind, info.IsDir(), lifetime)
}

// Unshare removes the link of the given kind for the item at name. The item
// need not exist any more, so a link can be removed after its item was changed
// by other means.
func (a *Access) Unshare(ctx context.Context, name string, kind share.Kind) error {
	if err := storage.ValidatePath(name); err != nil {
		return err
	}
	if _, err := share.ParseKind(string(kind)); err != nil {
		return err
	}
	return a.service.shares.Revoke(ctx, a.user.ID, name, kind)
}

// statItem describes the item at name. Unlike Stat, it rejects the top folder,
// which cannot be shared.
func (a *Access) statItem(ctx context.Context, name string) (fs.FileInfo, error) {
	if err := storage.ValidatePath(name); err != nil {
		return nil, err
	}
	return a.Stat(ctx, name)
}

// UploadLink returns the upload link with token secret. Its error wraps
// share.ErrNotFound when the link is gone or has expired, or is a download
// link.
func (s *Service) UploadLink(ctx context.Context, secret string) (share.Link, error) {
	link, err := s.shares.Find(ctx, secret)
	if err == nil && link.Kind != share.Upload {
		return share.Link{}, share.ErrNotFound
	}
	return link, err
}

// currentLink returns the link with token secret if it is still an upload
// link to the folder an upload started with, or share.ErrNotFound. The link
// may have been revoked, replaced, or expired, or its folder renamed, moved, or
// deleted, while files streamed.
func (s *Service) currentLink(ctx context.Context, secret string, started share.Link) (share.Link, error) {
	link, err := s.UploadLink(ctx, secret)
	if err != nil {
		return share.Link{}, err
	}
	if link.User.ID != started.User.ID || link.Path != started.Path {
		return share.Link{}, fmt.Errorf("%w: the link changed during the upload", share.ErrNotFound)
	}
	return link, nil
}
