package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewProviderFromConfiguration(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "uploads")
	if err := (Configuration{Directory: dir}).Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("validation created the directory: %v", err)
	}
	p, err := NewProviderFromConfiguration(Configuration{Directory: dir})
	if err != nil || p.Directory() != dir {
		t.Fatalf("provider = %#v, %v", p, err)
	}
	if _, err := NewProviderFromConfiguration(Configuration{}); err == nil {
		t.Fatal("a configuration without a directory created a provider")
	}
}
