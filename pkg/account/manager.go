package account

import (
	"context"
	"fmt"

	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/storage"
)

// Manager changes accounts on behalf of an administrator. It coordinates the
// database, the account locks, the usage cache, and the account's files, so
// that callers need not know which a change involves. It has no HTTP or
// template concerns.
type Manager struct {
	store   *Store
	locks   *Locks
	usage   *quota.Cache
	storage storage.Provider
}

// NewManager returns a manager for the accounts in store, whose files are in
// provider.
func NewManager(store *Store, locks *Locks, usage *quota.Cache, provider storage.Provider) *Manager {
	return &Manager{store: store, locks: locks, usage: usage, storage: provider}
}

// Credentials are a generated passphrase for an account. They are shown once.
type Credentials struct {
	User       User
	Passphrase string
}

func issueCredentials(save func(string) (User, error)) (Credentials, error) {
	passphrase, err := GeneratePassphrase()
	if err != nil {
		return Credentials{}, fmt.Errorf("%w: %v", ErrPassphraseGeneration, err)
	}
	u, err := save(passphrase)
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{User: u, Passphrase: passphrase}, nil
}

func requireAdministrator(actor User) error {
	if !actor.Admin {
		return ErrAdministratorRequired
	}
	return nil
}

// Create adds an account with a generated passphrase.
func (m *Manager) Create(ctx context.Context, actor User, username string, admin bool, quotaBytes int64) (Credentials, error) {
	if err := requireAdministrator(actor); err != nil {
		return Credentials{}, err
	}
	return issueCredentials(func(password string) (User, error) {
		return m.store.Create(ctx, username, password, admin, quotaBytes)
	})
}

// Recycle restores deleted account id with a generated passphrase, keeping its
// files. It waits for work started before the deletion to finish.
func (m *Manager) Recycle(ctx context.Context, actor User, id int64) (Credentials, error) {
	if err := requireAdministrator(actor); err != nil {
		return Credentials{}, err
	}
	unlock, err := m.locks.LockForRemoval(ctx, id)
	if err != nil {
		return Credentials{}, err
	}
	defer unlock()
	return issueCredentials(func(password string) (User, error) {
		return m.store.Recycle(ctx, id, password)
	})
}

// ResetPassword gives active account id a generated passphrase and signs it
// out everywhere.
func (m *Manager) ResetPassword(ctx context.Context, actor User, id int64) (Credentials, error) {
	if err := requireAdministrator(actor); err != nil {
		return Credentials{}, err
	}
	return issueCredentials(func(password string) (User, error) {
		return m.store.ResetPassword(ctx, id, password)
	})
}

// Delete disables account id and keeps its files.
func (m *Manager) Delete(ctx context.Context, actor User, id int64) error {
	if err := requireAdministrator(actor); err != nil {
		return err
	}
	return m.store.Delete(ctx, id, actor.ID)
}

// SetQuota changes account id's quota while no change to its files is in
// progress.
func (m *Manager) SetQuota(ctx context.Context, actor User, id, quotaBytes int64) error {
	if err := requireAdministrator(actor); err != nil {
		return err
	}
	unlock, err := m.locks.Lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	return m.store.SetQuota(ctx, id, quotaBytes)
}

// Purge permanently removes deleted account id and its files, releasing its
// username. The account stays reserved until all its files have been removed,
// so a failed purge can be retried. The lock also serializes purging with
// recycling and with uploads still in progress.
func (m *Manager) Purge(ctx context.Context, actor User, id int64) error {
	if err := requireAdministrator(actor); err != nil {
		return err
	}
	unlock, err := m.locks.LockForRemoval(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	purge, err := m.store.BeginPurge(ctx, id)
	if err != nil {
		return err
	}
	defer purge.Rollback()
	err = m.storage.Remove(ctx, purge.Slug)
	m.usage.Forget(id) // Even a failed removal may have deleted some files.
	if err != nil {
		return err
	}
	return purge.Commit()
}
