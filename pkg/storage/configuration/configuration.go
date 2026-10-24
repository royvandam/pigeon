// Package configuration creates storage providers from configuration. It is
// separate from package storage so that providers, which implement storage's
// interfaces, can be chosen here without import cycles. Each provider defines
// its own configuration type and factory; this package chooses between them.
package configuration

import (
	"errors"
	"fmt"

	"github.com/royvandam/pigeon/pkg/storage"
	"github.com/royvandam/pigeon/pkg/storage/local"
)

// Configuration chooses a storage provider: exactly one field is set.
type Configuration struct {
	// Local stores files in folders on the local filesystem.
	Local *local.Configuration `json:"local"`
}

// Validate checks cfg without creating anything.
func (cfg Configuration) Validate() error {
	switch {
	case cfg.Local != nil:
		if err := cfg.Local.Validate(); err != nil {
			return fmt.Errorf("local: %w", err)
		}
		return nil
	default:
		return errors.New("choose a storage provider, such as local")
	}
}

// NewProviderFromConfiguration creates the provider that cfg chooses.
func NewProviderFromConfiguration(cfg Configuration) (storage.Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	provider, err := local.NewProviderFromConfiguration(*cfg.Local)
	if err != nil {
		return nil, fmt.Errorf("local: %w", err)
	}
	return provider, nil
}
