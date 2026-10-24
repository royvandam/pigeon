package local

import "errors"

// Configuration configures a Provider.
type Configuration struct {
	// Directory holds one folder per account. A relative directory is
	// resolved from the working directory at startup.
	Directory string `json:"directory"`
}

// Validate checks cfg without creating anything.
func (cfg Configuration) Validate() error {
	if cfg.Directory == "" {
		return errors.New("storage directory cannot be empty")
	}
	return nil
}

// NewProviderFromConfiguration creates the provider that cfg describes.
func NewProviderFromConfiguration(cfg Configuration) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return NewProvider(cfg.Directory)
}
