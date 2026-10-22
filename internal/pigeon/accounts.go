package pigeon

import (
	"context"
	"errors"

	"github.com/royvandam/pigeon/pkg/account"
)

// accountCommand is an account change requested on the admin page.
type accountCommand struct {
	action        string
	id            int64
	username      string
	administrator bool
	quota         int64
}

type accountChange struct {
	Credentials          *account.Credentials
	ActorPasswordChanged bool
}

// applyAccountCommand performs command through the account manager on behalf
// of actor.
func (cfg config) applyAccountCommand(ctx context.Context, actor account.User, command accountCommand) (accountChange, error) {
	var change accountChange
	var credentials account.Credentials
	var err error
	switch command.action {
	case "create":
		credentials, err = cfg.manager.Create(ctx, actor, command.username, command.administrator, command.quota)
	case "recycle":
		credentials, err = cfg.manager.Recycle(ctx, actor, command.id)
	case "purge":
		err = cfg.manager.Purge(ctx, actor, command.id)
	case "delete":
		err = cfg.manager.Delete(ctx, actor, command.id)
	case "quota":
		err = cfg.manager.SetQuota(ctx, actor, command.id, command.quota)
	case "reset":
		credentials, err = cfg.manager.ResetPassword(ctx, actor, command.id)
		change.ActorPasswordChanged = err == nil && command.id == actor.ID
	default:
		err = errors.New("unknown action")
	}
	if err == nil && credentials.Passphrase != "" {
		change.Credentials = &credentials
	}
	return change, err
}
