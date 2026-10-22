package pigeon

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/royvandam/pigeon/pkg/quota"
)

func TestSettingsAndUsersSurviveRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.db")
	s, err := openStore(ctx, path, testPasswordCost)
	if err != nil {
		t.Fatal(err)
	}
	want := settings{dir: "/tmp/saved-uploads", listen: "127.0.0.1:9000"}
	if err := s.saveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if _, err := s.accounts.Create(ctx, "Admin", testPassword, true, quota.DefaultQuota); err != nil {
		t.Fatal(err)
	}
	var hash []byte
	if err := s.db.QueryRow(`SELECT password_hash FROM users`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(hash, []byte(testPassword)) || !bytes.HasPrefix(hash, []byte("$2")) {
		t.Fatal("password was not stored as a bcrypt hash")
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(ctx, path, testPasswordCost)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	got, err := s.loadSettings(ctx)
	if err != nil || got != want {
		t.Fatalf("settings after restart = %#v, %v; want %#v", got, err, want)
	}
	u, err := s.accounts.Authenticate(ctx, " ADMIN ", testPassword)
	if err != nil || !u.Admin {
		t.Fatalf("admin did not survive restart: %#v, %v", u, err)
	}
}

func TestInitialAdminPromptOnlyRunsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := openStore(ctx, filepath.Join(t.TempDir(), "setup.db"), testPasswordCost)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	var out bytes.Buffer
	input := strings.NewReader("Initial Admin\nfirst-password\nmismatch\nInitial Admin\n" + testPassword + "\n" + testPassword + "\n")
	if err := promptInitialAdmin(ctx, s.accounts, input, &out, nil); err != nil {
		t.Fatal(err)
	}
	u, err := s.accounts.Authenticate(ctx, "Initial Admin", testPassword)
	if err != nil || !u.Admin {
		t.Fatalf("initial admin = %#v, %v", u, err)
	}
	if strings.Contains(out.String(), testPassword) {
		t.Fatal("prompt printed the password")
	}
	out.Reset()
	if err := promptInitialAdmin(ctx, s.accounts, strings.NewReader(""), &out, nil); err != nil || out.Len() != 0 {
		t.Fatalf("setup repeated after admin creation: %v, %q", err, out.String())
	}
}

func TestInitialAdminRequiresCompleteInput(t *testing.T) {
	t.Parallel()
	s, err := openStore(context.Background(), filepath.Join(t.TempDir(), "setup.db"), testPasswordCost)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	var out bytes.Buffer
	if err := promptInitialAdmin(context.Background(), s.accounts, strings.NewReader(""), &out, nil); err == nil {
		t.Fatal("startup accepted missing administrator input")
	}
	exists, err := s.accounts.HasUsers(context.Background())
	if err != nil || exists {
		t.Fatalf("incomplete setup created an account: %t, %v", exists, err)
	}
}

func TestInitialAdminPromptRejectsShortPasswords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := openStore(ctx, filepath.Join(t.TempDir(), "setup.db"), testPasswordCost)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	var out bytes.Buffer
	input := strings.NewReader("Admin\nelevenchars\nelevenchars\nAdmin\n" + testPassword + "\n" + testPassword + "\n")
	if err := promptInitialAdmin(ctx, s.accounts, input, &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "at least 12 characters and at most 72 bytes") {
		t.Fatalf("short password was not explained: %q", out.String())
	}
	if _, err := s.accounts.Authenticate(ctx, "Admin", "elevenchars"); err == nil {
		t.Fatal("the short password was saved")
	}
	if _, err := s.accounts.Authenticate(ctx, "Admin", testPassword); err != nil {
		t.Fatalf("retried password: %v", err)
	}
}
