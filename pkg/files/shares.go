package files

import (
	"context"
	"io/fs"

	"github.com/royvandam/pigeon/pkg/storage"
)

// Shared reports whether the item at name has a share link.
func (a *Access) Shared(ctx context.Context, name string) (bool, error) {
	if _, err := a.statItem(ctx, name); err != nil {
		return false, err
	}
	return a.service.shares.Active(ctx, a.user, name)
}

// Share creates a share link for the item at name, replacing any link it had,
// and returns the link's token.
func (a *Access) Share(ctx context.Context, name string) (string, error) {
	info, err := a.statItem(ctx, name)
	if err != nil {
		return "", err
	}
	return a.service.shares.Create(ctx, a.user, name, info.IsDir())
}

// Unshare removes the share link of the item at name. The item need not exist
// any more, so a link can be removed after its item was changed by other means.
func (a *Access) Unshare(ctx context.Context, name string) error {
	if err := storage.ValidatePath(name); err != nil {
		return err
	}
	return a.service.shares.Revoke(ctx, a.user.ID, name)
}

// statItem describes the item at name. Unlike Stat, it rejects the top folder,
// which cannot be shared.
func (a *Access) statItem(ctx context.Context, name string) (fs.FileInfo, error) {
	if err := storage.ValidatePath(name); err != nil {
		return nil, err
	}
	return a.Stat(ctx, name)
}
