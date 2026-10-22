// Package configuration creates storage providers from configuration. It is
// separate from package storage so that providers, which implement storage's
// interfaces, can be chosen here without import cycles.
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
	Local *LocalConfiguration `json:"local"`
}

// LocalConfiguration configures local.Provider.
type LocalConfiguration struct {
	// Directory holds one folder per account. A relative directory is
	// resolved from the working directory at startup.
	Directory string `json:"directory"`
}

// Validate checks cfg without creating anything.
func (cfg Configuration) Validate() error {
	switch {
	case cfg.Local != nil:
		if cfg.Local.Directory == "" {
			return errors.New("local: storage directory cannot be empty")
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
	provider, err := local.NewProvider(cfg.Local.Directory)
	if err != nil {
		return nil, fmt.Errorf("local: %w", err)
	}
	return provider, nil
}
