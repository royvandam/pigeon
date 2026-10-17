package pigeon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPassword = "test-password"

func newTestConfig(t *testing.T, quota int64) config {
	t.Helper()
	root := t.TempDir()
	s, err := openStore(context.Background(), filepath.Join(root, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	if _, err := s.createUserWithQuota(context.Background(), "Test User", testPassword, false, quota); err != nil {
		t.Fatal(err)
	}
	// A one-byte allocation unit keeps quota arithmetic in tests exact; tests of
	// the production unit set allocationUnit explicitly.
	return config{dir: filepath.Join(root, "uploads"), store: s, allocationUnit: 1}
}

func readUploadDir(root, slug string) ([]os.DirEntry, error) {
	files, err := os.ReadDir(filepath.Join(root, slug))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return files, err
}

func TestSettingsAndUsersSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.db")
	s, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	want := settings{dir: "/tmp/saved-uploads", listen: "127.0.0.1:9000"}
	if err := s.saveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if _, err := s.createUser(ctx, "Admin", testPassword, true); err != nil {
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
	s, err = openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	got, err := s.loadSettings(ctx)
	if err != nil || got != want {
		t.Fatalf("settings after restart = %#v, %v; want %#v", got, err, want)
	}
	u, err := s.authenticate(ctx, " ADMIN ", testPassword)
	if err != nil || !u.Admin {
		t.Fatalf("admin did not survive restart: %#v, %v", u, err)
	}
}

func TestUsernameNormalization(t *testing.T) {
	for _, tt := range []struct{ username, slug string }{
		{" Roy Van Dijk ", "roy-van-dijk"},
		{"Élise_Müller", "elise-muller"},
		{"ＲＯＹ", "roy"},
		{"../../Alice", "alice"},
		{"中文 用户", "中文-用户"},
		{"Straße", "strasse"},
	} {
		_, _, got, err := normalizeUsername(tt.username)
		if err != nil || got != tt.slug {
			t.Errorf("normalizeUsername(%q) = %q, %v; want %q", tt.username, got, err, tt.slug)
		}
	}
	for _, input := range []string{"", "...", "../", "user\x00name", "user\u202ename", strings.Repeat("x", 65)} {
		if _, _, _, err := normalizeUsername(input); err == nil {
			t.Errorf("accepted invalid username %q", input)
		}
	}
}

func TestUsernameAndFolderCollisionsStayReserved(t *testing.T) {
	cfg := newTestConfig(t, 100)
	ctx := context.Background()
	u, err := cfg.store.createUser(ctx, "Élise Smith", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ÉLISE SMITH", "Elise_Smith"} {
		if _, err := cfg.store.createUser(ctx, name, testPassword, false); !errors.Is(err, errUserConflict) {
			t.Fatalf("create %q = %v; want collision", name, err)
		}
	}
	if err := cfg.store.deleteUser(ctx, u.ID, 999); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.store.createUser(ctx, "Elise Smith", testPassword, false); !errors.Is(err, errUserConflict) {
		t.Fatalf("deleted folder was reused: %v", err)
	}
}

func TestInitialAdminPromptOnlyRunsOnce(t *testing.T) {
	ctx := context.Background()
	s, err := openStore(ctx, filepath.Join(t.TempDir(), "setup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	var out bytes.Buffer
	input := strings.NewReader("Initial Admin\nfirst-password\nmismatch\nInitial Admin\n" + testPassword + "\n" + testPassword + "\n")
	if err := promptInitialAdmin(ctx, s, input, &out, nil); err != nil {
		t.Fatal(err)
	}
	u, err := s.authenticate(ctx, "Initial Admin", testPassword)
	if err != nil || !u.Admin {
		t.Fatalf("initial admin = %#v, %v", u, err)
	}
	if strings.Contains(out.String(), testPassword) {
		t.Fatal("prompt printed the password")
	}
	out.Reset()
	if err := promptInitialAdmin(ctx, s, strings.NewReader(""), &out, nil); err != nil || out.Len() != 0 {
		t.Fatalf("setup repeated after admin creation: %v, %q", err, out.String())
	}
}

func TestInitialAdminRequiresCompleteInput(t *testing.T) {
	s, err := openStore(context.Background(), filepath.Join(t.TempDir(), "setup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	var out bytes.Buffer
	if err := promptInitialAdmin(context.Background(), s, strings.NewReader(""), &out, nil); err == nil {
		t.Fatal("startup accepted missing administrator input")
	}
	exists, err := s.hasUsers(context.Background())
	if err != nil || exists {
		t.Fatalf("incomplete setup created an account: %t, %v", exists, err)
	}
}

func TestInitialAdminPromptRejectsShortPasswords(t *testing.T) {
	ctx := context.Background()
	s, err := openStore(ctx, filepath.Join(t.TempDir(), "setup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	var out bytes.Buffer
	input := strings.NewReader("Admin\nelevenchars\nelevenchars\nAdmin\n" + testPassword + "\n" + testPassword + "\n")
	if err := promptInitialAdmin(ctx, s, input, &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "at least 12 characters and at most 72 bytes") {
		t.Fatalf("short password was not explained: %q", out.String())
	}
	if _, err := s.authenticate(ctx, "Admin", "elevenchars"); err == nil {
		t.Fatal("the short password was saved")
	}
	if _, err := s.authenticate(ctx, "Admin", testPassword); err != nil {
		t.Fatalf("retried password: %v", err)
	}
}
