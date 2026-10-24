package files

import (
	"context"
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
