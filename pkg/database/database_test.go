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
	db, err := Open(context.Background(), Configuration{Path: path})
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
	if _, err := Open(ctx, Configuration{Path: path}); err == nil || !strings.Contains(err.Error(), "schema version 99") {
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
	if _, err := Open(ctx, Configuration{Path: path}); err == nil || !strings.Contains(err.Error(), "no schema version") {
		t.Fatalf("opened a database without a schema version: %v", err)
	}
	if _, err := Open(ctx, Configuration{}); err == nil {
		t.Fatal("opened a database without a path")
	}
}

func TestIsUniqueViolation(t *testing.T) {
	t.Parallel()
	db := open(t, filepath.Join(t.TempDir(), "pigeon.db"))
	insert := func(username string, quota int64) error {
		_, err := db.Exec(`INSERT INTO users (username, username_key, slug, password_hash, is_admin, quota_bytes) VALUES (?, ?, ?, '', 0, ?)`,
			username, username, username, quota)
		return err
	}
	if err := insert("jane", 1); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert("jane", 1); !IsUniqueViolation(err) {
		t.Fatalf("duplicate insert: %v; want a unique violation", err)
	}
	if err := insert("john", 0); err == nil || IsUniqueViolation(err) {
		t.Fatalf("other constraint: %v", err)
	}
}

// schemaVersion1 is the schema of the first release, before share links had
// kinds or lifetimes.
const schemaVersion1 = `
CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL,
  username_key TEXT NOT NULL UNIQUE,
  slug TEXT NOT NULL UNIQUE,
  password_hash BLOB NOT NULL,
  is_admin INTEGER NOT NULL CHECK (is_admin IN (0, 1)),
  auth_version INTEGER NOT NULL DEFAULT 0,
  quota_bytes INTEGER NOT NULL CHECK (quota_bytes > 0),
  deleted_at INTEGER
);
CREATE TABLE sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  user_version INTEGER NOT NULL,
  csrf_token TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX sessions_user_id ON sessions(user_id);
CREATE INDEX sessions_expires_at ON sessions(expires_at);
CREATE TABLE shares (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  is_directory INTEGER NOT NULL CHECK (is_directory IN (0, 1)),
  created_at INTEGER NOT NULL,
  UNIQUE (user_id, path)
);
PRAGMA user_version = 1;
`

func TestVersion1DatabasesKeepTheirLinksAsDownloadLinks(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pigeon.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(schemaVersion1 + `
INSERT INTO users (username, username_key, slug, password_hash, is_admin, quota_bytes) VALUES ('Jane', 'jane', 'jane', X'00', 0, 100);
INSERT INTO shares (token_hash, user_id, path, is_directory, created_at) VALUES ('hash', 1, 'docs', 1, 1000);`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	db := open(t, path)
	if version := userVersion(t, db); version != SchemaVersion {
		t.Fatalf("converted database has schema version %d, want %d", version, SchemaVersion)
	}
	var kind string
	var expires sql.NullInt64
	if err := db.QueryRow(`SELECT kind, expires_at FROM shares WHERE token_hash = 'hash' AND path = 'docs' AND is_directory = 1 AND created_at = 1000`).Scan(&kind, &expires); err != nil || kind != "download" || expires.Valid {
		t.Fatalf("converted link: kind %q, expires %v, %v", kind, expires, err)
	}
	// The folder can now have an upload link beside its download link, but a
	// file cannot have one.
	if _, err := db.Exec(`INSERT INTO shares (token_hash, user_id, path, kind, is_directory, created_at) VALUES ('upload', 1, 'docs', 'upload', 1, 2000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO shares (token_hash, user_id, path, kind, is_directory, created_at) VALUES ('file', 1, 'a.txt', 'upload', 0, 2000)`); err == nil {
		t.Fatal("a file got an upload link")
	}
	if len(upgrades) != SchemaVersion-1 {
		t.Fatalf("%d upgrades for schema version %d", len(upgrades), SchemaVersion)
	}
}
