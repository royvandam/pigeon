package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/royvandam/pigeon/pkg/database"
	"github.com/royvandam/pigeon/pkg/server"
	storageconfiguration "github.com/royvandam/pigeon/pkg/storage/configuration"
)

// legacyDatabase creates a database with the server settings an earlier
// version saved.
func legacyDatabase(t *testing.T, uploads, listen, proxies, publicURL string) *sql.DB {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "pigeon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE settings (
  id INTEGER PRIMARY KEY CHECK (id = 1), uploads_dir TEXT NOT NULL, listen TEXT NOT NULL,
  trusted_proxies TEXT NOT NULL DEFAULT '', public_url TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO settings VALUES (1, ?, ?, ?, ?)`, uploads, listen, proxies, publicURL); err != nil {
		t.Fatal(err)
	}
	return db
}

func localConfiguration(directory string, http server.Configuration) ApplicationConfiguration {
	return ApplicationConfiguration{
		Storage: storageconfiguration.Configuration{Local: &storageconfiguration.LocalConfiguration{Directory: directory}},
		HTTP:    http,
	}
}

func TestMatchingLegacySettingsAreRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	uploads := filepath.Join(t.TempDir(), "uploads")
	db := legacyDatabase(t, uploads, "127.0.0.1:8080", "127.0.0.1, ::1,", "https://Files.Example.com/")
	// The values match as the server applies them, not as they are written.
	cfg := localConfiguration(uploads+"/", server.Configuration{
		PublicURL: "https://files.example.com", TrustedProxies: []string{"127.0.0.1/32", "::1"},
	})
	if err := checkLegacySettings(ctx, db, cfg); err != nil {
		t.Fatal(err)
	}
	if saved, err := database.ReadLegacySettings(ctx, db); err != nil || saved != nil {
		t.Fatalf("matching settings remain: %#v, %v", saved, err)
	}
	// Once they are gone, the configuration may change.
	if err := checkLegacySettings(ctx, db, localConfiguration(t.TempDir(), server.Configuration{ListenAddress: ":9000"})); err != nil {
		t.Fatalf("a later change was refused: %v", err)
	}
}

func TestDifferentLegacySettingsStopStartup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	uploads := filepath.Join(t.TempDir(), "uploads")
	db := legacyDatabase(t, uploads, "0.0.0.0:8080", "10.0.0.1", "https://files.example.com")
	for name, cfg := range map[string]ApplicationConfiguration{
		"defaults":                 localConfiguration(t.TempDir(), server.Configuration{}),
		"another storage provider": {HTTP: server.Configuration{ListenAddress: "0.0.0.0:8080", TrustedProxies: []string{"10.0.0.1"}, PublicURL: "https://files.example.com"}},
	} {
		err := checkLegacySettings(ctx, db, cfg)
		if err == nil {
			t.Fatalf("%s: started with different settings", name)
		}
		for _, want := range []string{`storage.local.directory: "` + uploads + `"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q does not show %s", name, err, want)
			}
		}
	}
	err := checkLegacySettings(ctx, db, localConfiguration(t.TempDir(), server.Configuration{}))
	for _, want := range []string{
		`http.listenAddress: "0.0.0.0:8080" (configured: "127.0.0.1:8080")`,
		`http.publicURL: "https://files.example.com" (configured: "")`,
		`http.trustedProxies: ["10.0.0.1"] (configured: [])`,
		"Set them there to continue",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not show %s", err, want)
		}
	}
	if saved, err := database.ReadLegacySettings(ctx, db); err != nil || saved == nil {
		t.Fatalf("refused settings were removed: %#v, %v", saved, err)
	}
}
