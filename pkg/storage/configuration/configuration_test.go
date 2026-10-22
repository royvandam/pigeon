package configuration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/royvandam/pigeon/pkg/storage/local"
)

func TestNewProviderFromConfiguration(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "uploads")
	provider, err := NewProviderFromConfiguration(Configuration{Local: &LocalConfiguration{Directory: dir}})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := provider.(*local.Provider); !ok || p.Directory() != dir {
		t.Fatalf("provider = %#v", provider)
	}
	if _, err := NewProviderFromConfiguration(Configuration{}); err == nil || !strings.Contains(err.Error(), "choose a storage provider") {
		t.Fatalf("no provider: %v", err)
	}
	if _, err := NewProviderFromConfiguration(Configuration{Local: &LocalConfiguration{}}); err == nil || !strings.HasPrefix(err.Error(), "local: ") {
		t.Fatalf("local provider without a directory: %v", err)
	}
}

func TestValidateCreatesNothing(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "uploads")
	if err := (Configuration{Local: &LocalConfiguration{Directory: dir}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("validation created the directory: %v", err)
	}
	for _, cfg := range []Configuration{{}, {Local: &LocalConfiguration{}}} {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%+v passed validation", cfg)
		}
	}
}
