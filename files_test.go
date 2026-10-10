package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStoredFile(t *testing.T, cfg config, slug, name, contents string) {
	t.Helper()
	filename := filepath.Join(cfg.dir, slug, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestExplorerFileAndFolderLifecycle(t *testing.T) {
	cfg := newTestConfig(t, 10)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	handler := newServer(cfg).Handler
	action := func(form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("csrf_token", csrf)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, formRequest("/files", form, cookie))
		if w.Code != http.StatusSeeOther {
			t.Fatalf("file action %v: %d, %s", form, w.Code, w.Body)
		}
		return w
	}
	get := func(location string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, location, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d, %s", location, w.Code, w.Body)
		}
		return w
	}

	action(url.Values{"action": {"mkdir"}, "name": {"My folder"}})
	action(url.Values{"action": {"mkdir"}, "dir": {"My folder"}, "name": {"写真"}})
	r := uploadFilesRequest(t, uploadFile{"readme ?.txt", "12345"}, uploadFile{"project/docs/a.txt", "67890"})
	r.URL.RawQuery = url.Values{"dir": {"My folder/写真"}}.Encode()
	r.Header.Del("Authorization")
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || uploadResult(t, w, "saved_as") != "My folder/写真" || w.Header().Get("X-Quota-Remaining") != "0" {
		t.Fatalf("upload to selected folder: %d, %s", w.Code, w.Body)
	}
	listing := get("/files?" + url.Values{"dir": {"My folder/写真"}}.Encode()).Body.String()
	if !strings.Contains(listing, "project</span>") || !strings.Contains(listing, "readme ?.txt</span>") || strings.Index(listing, "project</span>") > strings.Index(listing, "readme ?.txt</span>") {
		t.Fatalf("explorer should list folders first and preserve names: %s", listing)
	}
	page := get("/?" + url.Values{"dir": {"My folder/写真"}}.Encode()).Body.String()
	for _, text := range []string{"My files", "My folder", "写真", "Upload to:", "/My folder/写真", "Download ZIP", "Rename", "Move", "Delete"} {
		if !strings.Contains(page, text) {
			t.Errorf("explorer page missing %q", text)
		}
	}

	action(url.Values{"action": {"rename"}, "dir": {"My folder/写真"}, "path": {"My folder/写真/readme ?.txt"}, "name": {"guide.txt"}})
	action(url.Values{"action": {"move"}, "dir": {"My folder/写真"}, "path": {"My folder/写真/guide.txt"}, "destination": {""}})
	if got := get("/download?path=guide.txt").Body.String(); got != "12345" {
		t.Fatalf("moved file contents = %q", got)
	}
	action(url.Values{"action": {"rename"}, "path": {"My folder"}, "name": {"Archive"}})
	action(url.Values{"action": {"mkdir"}, "name": {"destination"}})
	action(url.Values{"action": {"move"}, "path": {"Archive"}, "destination": {"destination"}})
	if got := get("/download?" + url.Values{"path": {"destination/Archive/写真/project/docs/a.txt"}}.Encode()).Body.String(); got != "67890" {
		t.Fatalf("moved folder contents = %q", got)
	}
	w = action(url.Values{"action": {"delete"}, "dir": {"destination/Archive"}, "path": {"destination/Archive"}})
	if w.Header().Get("Location") != "/?result=delete" {
		t.Fatalf("deleting viewed directory should return to root: %s", w.Header().Get("Location"))
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, uploadRequest(t, "new.txt", "abcde"))
	if w.Code != http.StatusOK || w.Header().Get("X-Quota-Used") != "10" {
		t.Fatalf("recursive deletion should free quota: %d, %s", w.Code, w.Body)
	}
	action(url.Values{"action": {"delete"}, "path": {"guide.txt"}})
	if _, err := os.Stat(filepath.Join(cfg.dir, "test-user", "guide.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted file remains: %v", err)
	}
}

func TestFileActionsRejectUnsafePathsAndConflicts(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "folder/sub/a.txt", "original")
	writeStoredFile(t, cfg, "test-user", "taken.txt", "taken")
	for _, tt := range []struct {
		form   url.Values
		status int
	}{
		{url.Values{"action": {"delete"}, "path": {""}}, 400},
		{url.Values{"action": {"delete"}, "path": {"."}}, 400},
		{url.Values{"action": {"delete"}, "path": {"../other-user"}}, 400},
		{url.Values{"action": {"delete"}, "path": {"/tmp"}}, 400},
		{url.Values{"action": {"mkdir"}, "name": {"../outside"}}, 400},
		{url.Values{"action": {"mkdir"}, "name": {".upload-secret"}}, 400},
		{url.Values{"action": {"mkdir"}, "dir": {"../other-user"}, "name": {"bad"}}, 400},
		{url.Values{"action": {"mkdir"}, "name": {"taken.txt"}}, 409},
		{url.Values{"action": {"rename"}, "path": {"folder"}, "name": {"taken.txt"}}, 409},
		{url.Values{"action": {"rename"}, "path": {"taken.txt"}, "name": {"../outside"}}, 400},
		{url.Values{"action": {"move"}, "path": {"folder"}, "destination": {"folder/sub"}}, 400},
		{url.Values{"action": {"move"}, "path": {"taken.txt"}, "destination": {"../other-user"}}, 400},
		{url.Values{"action": {"move"}, "path": {"taken.txt"}, "destination": {"missing"}}, 404},
		{url.Values{"action": {"move"}, "path": {"folder"}, "destination": {"taken.txt"}}, 409},
		{url.Values{"action": {"delete"}, "path": {"missing"}}, 404},
		{url.Values{"action": {"unknown"}, "path": {"taken.txt"}}, 400},
	} {
		tt.form.Set("csrf_token", csrf)
		w := httptest.NewRecorder()
		cfg.handleFiles(w, formRequest("/files", tt.form, cookie))
		if w.Code != tt.status {
			t.Errorf("action %v: %d, %s; want %d", tt.form, w.Code, w.Body, tt.status)
		}
	}
	for _, name := range []string{"folder/sub/a.txt", "taken.txt"} {
		if _, err := os.Stat(filepath.Join(cfg.dir, "test-user", filepath.FromSlash(name))); err != nil {
			t.Fatalf("rejected operation changed stored file: %v", err)
		}
	}
	// A same-name rename is harmless and can be retried.
	w := httptest.NewRecorder()
	cfg.handleFiles(w, formRequest("/files", url.Values{"action": {"rename"}, "path": {"taken.txt"}, "name": {"taken.txt"}, "csrf_token": {csrf}}, cookie))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("same-name rename: %d, %s", w.Code, w.Body)
	}
}

func TestExplorerAccountIsolationAndCSRF(t *testing.T) {
	cfg := newTestConfig(t, 100)
	other, err := cfg.store.createUser(context.Background(), "Other User", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, cfg, other.Slug, "private.txt", "secret")
	writeStoredFile(t, cfg, "test-user", "visible.txt", "mine")
	cookie, csrf := sessionFor(t, cfg, "Test User")
	handler := newServer(cfg).Handler
	for _, location := range []string{"/files", "/download?path=private.txt", "/download?path=../other-user/private.txt", "/?dir=../other-user"} {
		r := httptest.NewRequest(http.MethodGet, location, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "private.txt</span>") {
			t.Fatalf("another user's files leaked via %s: %d, %s", location, w.Code, w.Body)
		}
		if location != "/files" && w.Code == http.StatusOK {
			t.Fatalf("another user's file path accepted: %s", location)
		}
	}
	for _, origin := range []bool{false, true} {
		form := url.Values{"action": {"delete"}, "path": {"visible.txt"}, "csrf_token": {"wrong"}}
		if origin {
			form.Set("csrf_token", csrf)
		}
		r := formRequest("/files", form, cookie)
		if origin {
			r.Header.Set("Origin", "https://attacker.example")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("invalid CSRF/origin accepted: %d", w.Code)
		}
	}
	for _, location := range []string{"/files", "/download?path=visible.txt"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, location, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", location, w.Code)
		}
	}
	if data, err := os.ReadFile(filepath.Join(cfg.dir, "test-user", "visible.txt")); err != nil || string(data) != "mine" {
		t.Fatalf("rejected deletion changed file: %q, %v", data, err)
	}
}
