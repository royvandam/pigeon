package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenCreatesAPrivateDatabaseWithTheSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "pigeon.db")
	db := open(t, path)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("database file: %v, %v", info, err)
	}
	for _, table := range []string{"users", "sessions", "shares"} {
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, table).Scan(&exists); err != nil || !exists {
			t.Errorf("table %s: %t, %v", table, exists, err)
		}
	}
	var foreignKeys int
	var journal string
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign keys = %d, %v", foreignKeys, err)
	}
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal mode = %q, %v", journal, err)
	}
	if db.Stats().MaxOpenConnections != 1 {
		t.Fatalf("max open connections = %d; one connection serializes transactions", db.Stats().MaxOpenConnections)
	}
}

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestDatabasesCarryTheirSchemaVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pigeon.db")
	db := open(t, path)
	if version := userVersion(t, db); version != SchemaVersion {
		t.Fatalf("new database has schema version %d, want %d", version, SchemaVersion)
	}
	if _, err := db.Exec(`INSERT INTO users (username, username_key, slug, password_hash, is_admin, quota_bytes) VALUES ('Jane', 'jane', 'jane', X'00', 0, 100)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	// Opening again keeps the data.
	db = open(t, path)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("users after reopening = %d, %v", count, err)
	}
	// A later release's database is refused rather than misread.
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "schema version 99") {
		t.Fatalf("opened a database from a later release: %v", err)
	}
}

func TestDatabasesWithoutASchemaVersionAreRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "unversioned.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "no schema version") {
		t.Fatalf("opened a database without a schema version: %v", err)
	}
	if _, err := Open(ctx, ""); err == nil {
		t.Fatal("opened a database without a path")
	}
}
