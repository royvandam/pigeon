package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/configuration"
	"github.com/royvandam/pigeon/pkg/database"
)

// input returns a file with contents, standing in for standard input.
func input(t *testing.T, contents string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func writeConfiguration(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pigeon.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunNeedsOneConfigurationFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, args := range [][]string{nil, {"a.json", "b.json"}} {
		if err := run(ctx, args, input(t, ""), io.Discard); err == nil || !strings.Contains(err.Error(), "configuration file") {
			t.Errorf("run(%q) = %v", args, err)
		}
	}
	if err := run(ctx, []string{"-h"}, input(t, ""), io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("help: %v", err)
	}
	// Flags of earlier versions now belong in the configuration file.
	if err := run(ctx, []string{"-db", "pigeon.db"}, input(t, ""), io.Discard); err == nil || !strings.Contains(err.Error(), "-db") {
		t.Errorf("an old flag: %v", err)
	}
}

func TestRunRejectsInvalidConfigurationsBeforeCreatingTheDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "pigeon.db")
	uploads := filepath.Join(dir, "uploads")
	for want, contents := range map[string]string{
		`unknown field "listen"`:        `{"database": {"path": "` + db + `"}, "http": {"listen": ":8080"}}`,
		"http: invalid listen address":  `{"database": {"path": "` + db + `"}, "storage": {"local": {"directory": "` + uploads + `"}}, "http": {"listenAddress": "nowhere"}}`,
		"storage: choose a storage":     `{"database": {"path": "` + db + `"}}`,
		"database: database path":       `{"storage": {"local": {"directory": "` + uploads + `"}}}`,
		`invalid duration "soon"`:       `{"http": {"uploadIdleTimeout": "soon"}}`,
		"http: public URL must be":      `{"storage": {"local": {"directory": "` + uploads + `"}}, "http": {"publicURL": "files.example.com"}}`,
		"http: trusted proxies must be": `{"storage": {"local": {"directory": "` + uploads + `"}}, "http": {"trustedProxies": ["proxy.example"]}}`,
	} {
		err := run(ctx, []string{writeConfiguration(t, contents)}, input(t, ""), io.Discard)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("configuration %s: %v; want an error with %q", contents, err, want)
		}
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatalf("an invalid configuration created the database: %v", err)
	}
}

func TestTheExampleConfigurationIsValid(t *testing.T) {
	t.Parallel()
	var cfg ApplicationConfiguration
	if err := configuration.LoadFile(filepath.Join("..", "..", "configs", "pigeon.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.HTTP.Validate(); err != nil || cfg.Database.Path == "" || cfg.Storage.Local == nil || cfg.Storage.Local.Directory == "" {
		t.Fatalf("example configuration = %+v, %v", cfg, err)
	}
}

func TestRunSetsUpAndServesUntilCanceled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db := filepath.Join(dir, "data", "pigeon.db")
	uploads := filepath.Join(dir, "data", "uploads")
	stale := filepath.Join(uploads, "jane", ".upload-0123456789ab", "part")
	if err := os.MkdirAll(filepath.Dir(stale), 0o750); err != nil {
		t.Fatal(err)
	}
	path := writeConfiguration(t, `{
  "database": {"path": "`+db+`"},
  "storage": {"local": {"directory": "`+uploads+`"}},
  "http": {"listenAddress": "127.0.0.1:0", "formReadTimeout": "10s"}
}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{path}, input(t, "Jane Doe\n"+testPassword+"\n"+testPassword+"\n"), io.Discard)
	}()
	// Wait for startup to remove the stale upload, which happens after setup
	// and before serving.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(filepath.Dir(stale)); os.IsNotExist(err) {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("run stopped early: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("startup did not finish")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run = %v after cancellation", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not stop after cancellation")
	}

	// First-run setup created the administrator in the configured database.
	opened, err := database.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	accounts, err := account.NewStore(opened, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := accounts.Authenticate(context.Background(), "jane doe", testPassword); err != nil || !u.Admin {
		t.Fatalf("initial administrator = %#v, %v", u, err)
	}
}
