package configuration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type example struct {
	Name    string   `json:"name"`
	Timeout Duration `json:"timeout"`
	Nested  struct {
		Enabled bool `json:"enabled"`
	} `json:"nested"`
}

func TestDecodeReadsDurationsAndNestedObjects(t *testing.T) {
	t.Parallel()
	var cfg example
	if err := Decode(strings.NewReader(`{"name": "x", "timeout": "1m30s", "nested": {"enabled": true}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "x" || time.Duration(cfg.Timeout) != 90*time.Second || !cfg.Nested.Enabled {
		t.Fatalf("decoded %+v", cfg)
	}
	if data, err := cfg.Timeout.MarshalJSON(); err != nil || string(data) != `"1m30s"` {
		t.Fatalf("marshaled duration = %s, %v", data, err)
	}
}

func TestDecodeRejectsUnknownFieldsAndMalformedValues(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		`{"nmae": "x"}`:                 `unknown field "nmae"`,
		`{"nested": {"enabeld": true}}`: `unknown field "enabeld"`,
		`{"timeout": 30}`:               `a duration must be a string`,
		`{"timeout": "30"}`:             `invalid duration "30"`,
		`{"name": 1}`:                   `example.name of type string`,
		`{"name": "x"} {"name": "y"}`:   `unexpected data after the configuration object`,
		`{"name": "x"`:                  `unexpected EOF`,
		``:                              `EOF`,
		`["not", "an", "object"]`:       `cannot unmarshal array`,
		`{"name": "x"} garbage`:         `unexpected data`,
	} {
		var cfg example
		if err := Decode(strings.NewReader(input), &cfg); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Decode(%q) = %v; want an error with %q", input, err, want)
		}
	}
}

func TestLoadFileNamesTheFileInErrors(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pigeon.json")
	if err := os.WriteFile(path, []byte(`{"unknown": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg example
	if err := LoadFile(path, &cfg); err == nil || !strings.HasPrefix(err.Error(), path+": ") {
		t.Fatalf("LoadFile = %v; want an error naming %s", err, path)
	}
	if err := LoadFile(filepath.Join(t.TempDir(), "missing.json"), &cfg); !os.IsNotExist(err) {
		t.Fatalf("missing file: %v", err)
	}
}
