package pigeon

import (
	"context"
	"errors"
	"fmt"
)

var (
	errPassphraseGeneration  = errors.New("failed to generate passphrase")
	errAdministratorRequired = errors.New("administrator access required")
)

// accountManager coordinates account changes without HTTP or template concerns.
// The store owns transactions; userStorage owns filesystem operations.
type accountManager struct {
	store     *store
	directory string
}

func (cfg config) accounts() accountManager {
	return accountManager{store: cfg.store, directory: cfg.dir}
}

type accountCommand struct {
	action        string
	id            int64
	username      string
	administrator bool
	quota         int64
}

type issuedCredentials struct {
	User       user
	Passphrase string
}

type accountChange struct {
	Credentials          *issuedCredentials
	ActorPasswordChanged bool
}

func issueUserCredentials(save func(string) (user, error)) (*issuedCredentials, error) {
	passphrase, err := generatePassphrase()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errPassphraseGeneration, err)
	}
	u, err := save(passphrase)
	if err != nil {
		return nil, err
	}
	return &issuedCredentials{User: u, Passphrase: passphrase}, nil
}

func (m accountManager) apply(ctx context.Context, actor user, command accountCommand) (accountChange, error) {
	var change accountChange
	if !actor.Admin {
		return change, errAdministratorRequired
	}
	var err error
	switch command.action {
	case "create":
		change.Credentials, err = issueUserCredentials(func(password string) (user, error) {
			return m.store.createUserWithQuota(ctx, command.username, password, command.administrator, command.quota)
		})
	case "recycle":
		change.Credentials, err = issueUserCredentials(func(password string) (user, error) {
			return m.store.recycleUser(ctx, command.id, password)
		})
	case "purge":
		err = m.purge(ctx, command.id)
	case "delete":
		err = m.store.deleteUser(ctx, command.id, actor.ID)
	case "quota":
		err = m.store.setQuota(ctx, command.id, command.quota)
	case "reset":
		change.Credentials, err = issueUserCredentials(func(password string) (user, error) {
			return m.store.resetPassword(ctx, command.id, password)
		})
		change.ActorPasswordChanged = err == nil && command.id == actor.ID
	default:
		err = errors.New("unknown action")
	}
	return change, err
}

func (m accountManager) purge(ctx context.Context, id int64) error {
	return m.store.purgeUser(ctx, id, func(slug string) error {
		return (userStorage{directory: m.directory, slug: slug}).purge()
	})
}
