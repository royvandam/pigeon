package pigeon

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
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
	s, err = openStore(ctx, path, testPasswordCost)
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	ctx := context.Background()
	s, err := openStore(ctx, filepath.Join(t.TempDir(), "setup.db"), testPasswordCost)
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
	t.Parallel()
	s, err := openStore(context.Background(), filepath.Join(t.TempDir(), "setup.db"), testPasswordCost)
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
	t.Parallel()
	ctx := context.Background()
	s, err := openStore(ctx, filepath.Join(t.TempDir(), "setup.db"), testPasswordCost)
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

func TestUnknownUsernamesCheckAHashOfTheConfiguredCost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if defaultPasswordCost != bcrypt.DefaultCost {
		t.Fatalf("production password cost = %d, want bcrypt's default %d", defaultPasswordCost, bcrypt.DefaultCost)
	}
	for _, cost := range []int{bcrypt.MinCost, bcrypt.MinCost + 1} {
		s, err := openStore(ctx, filepath.Join(t.TempDir(), "cost.db"), cost)
		if err != nil {
			t.Fatal(err)
		}
		defer s.db.Close()
		if _, err := s.createUser(ctx, "Test User", testPassword, false); err != nil {
			t.Fatal(err)
		}
		var stored []byte
		if err := s.db.QueryRow(`SELECT password_hash FROM users`).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		// Equal costs make a wrong password and an unknown username take as long.
		storedCost, storedErr := bcrypt.Cost(stored)
		dummyCost, dummyErr := bcrypt.Cost(s.passwords.dummy)
		if storedErr != nil || dummyErr != nil || storedCost != cost || dummyCost != cost {
			t.Fatalf("cost %d: stored hash cost %d (%v), dummy hash cost %d (%v)", cost, storedCost, storedErr, dummyCost, dummyErr)
		}
		if _, err := s.authenticate(ctx, "Nobody", testPassword); !errors.Is(err, errInvalidCredentials) {
			t.Fatalf("unknown username authenticated: %v", err)
		}
		if _, err := s.authenticate(ctx, "Test User", testPassword); err != nil {
			t.Fatalf("known username: %v", err)
		}
	}
}
