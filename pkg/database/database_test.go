package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/quota"
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

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name = ?)`, table, column).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
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

func TestOlderDatabasesAreMigrated(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "old.db")
	db := open(t, path)
	// Recreate the schema of a database from before per-account quotas and
	// hashed share tokens.
	plaintext := "QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"
	for _, statement := range []string{
		`ALTER TABLE users DROP COLUMN quota_bytes`,
		`INSERT INTO users (username, username_key, slug, password_hash, is_admin) VALUES ('Jane', 'jane', 'jane', X'00', 0)`,
		`DROP TABLE shares`,
		`CREATE TABLE shares (token TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  path TEXT NOT NULL, is_directory INTEGER NOT NULL CHECK (is_directory IN (0, 1)), created_at INTEGER NOT NULL, UNIQUE (user_id, path))`,
		`INSERT INTO shares (token, user_id, path, is_directory, created_at) SELECT '` + plaintext + `', id, 'readme.txt', 0, 0 FROM users`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	// Opening twice shows that migrations apply once and leave their results.
	for range 2 {
		db = open(t, path)
		var quotaBytes int64
		if err := db.QueryRow(`SELECT quota_bytes FROM users WHERE slug = 'jane'`).Scan(&quotaBytes); err != nil || quotaBytes != quota.DefaultQuota {
			t.Fatalf("migrated quota = %d, %v; want the default", quotaBytes, err)
		}
		var stored string
		if err := db.QueryRow(`SELECT token_hash FROM shares`).Scan(&stored); err != nil || stored != token.Hash(plaintext) {
			t.Fatalf("migrated share token = %q, %v; want its hash", stored, err)
		}
		if columnExists(t, db, "shares", "token") {
			t.Fatal("the plaintext token column remains")
		}
		db.Close()
	}
}
