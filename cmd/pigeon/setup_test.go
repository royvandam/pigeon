package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/database"
)

const testPassword = "test-password"

func newTestAccounts(t *testing.T) *account.Store {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "setup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	accounts, err := account.NewStore(db, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return accounts
}

func TestInitialAdminPromptOnlyRunsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	accounts := newTestAccounts(t)
	var out bytes.Buffer
	input := strings.NewReader("Initial Admin\nfirst-password\nmismatch\nInitial Admin\n" + testPassword + "\n" + testPassword + "\n")
	if err := promptInitialAdmin(ctx, accounts, input, &out, nil); err != nil {
		t.Fatal(err)
	}
	u, err := accounts.Authenticate(ctx, "Initial Admin", testPassword)
	if err != nil || !u.Admin {
		t.Fatalf("initial admin = %#v, %v", u, err)
	}
	if strings.Contains(out.String(), testPassword) {
		t.Fatal("prompt printed the password")
	}
	out.Reset()
	if err := promptInitialAdmin(ctx, accounts, strings.NewReader(""), &out, nil); err != nil || out.Len() != 0 {
		t.Fatalf("setup repeated after admin creation: %v, %q", err, out.String())
	}
}

func TestInitialAdminRequiresCompleteInput(t *testing.T) {
	t.Parallel()
	accounts := newTestAccounts(t)
	var out bytes.Buffer
	if err := promptInitialAdmin(context.Background(), accounts, strings.NewReader(""), &out, nil); err == nil {
		t.Fatal("startup accepted missing administrator input")
	}
	exists, err := accounts.HasUsers(context.Background())
	if err != nil || exists {
		t.Fatalf("incomplete setup created an account: %t, %v", exists, err)
	}
}

func TestInitialAdminPromptRejectsShortPasswords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	accounts := newTestAccounts(t)
	var out bytes.Buffer
	input := strings.NewReader("Admin\nelevenchars\nelevenchars\nAdmin\n" + testPassword + "\n" + testPassword + "\n")
	if err := promptInitialAdmin(ctx, accounts, input, &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "at least 12 characters and at most 72 bytes") {
		t.Fatalf("short password was not explained: %q", out.String())
	}
	if _, err := accounts.Authenticate(ctx, "Admin", "elevenchars"); err == nil {
		t.Fatal("the short password was saved")
	}
	if _, err := accounts.Authenticate(ctx, "Admin", testPassword); err != nil {
		t.Fatalf("retried password: %v", err)
	}
}
