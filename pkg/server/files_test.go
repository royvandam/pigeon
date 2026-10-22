package server

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/royvandam/pigeon/pkg/quota"
)

func TestExplorerFileAndFolderLifecycle(t *testing.T) {
	t.Parallel()
	// Two created folders, two folders added by the upload, ten file bytes, and
	// one unit left for the destination folder created later.
	srv := newTestServer(t, 15)
	cookie, csrf := sessionFor(t, srv, "Test User")
	handler := srv.Handler()
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
	if w.Code != http.StatusOK || uploadResult(t, w, "saved_as") != "My folder/写真" || w.Header().Get("X-Quota-Remaining") != "1" {
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
	// guide.txt, new.txt, and the remaining destination folder.
	if w.Code != http.StatusOK || w.Header().Get("X-Quota-Used") != "11" {
		t.Fatalf("recursive deletion should free quota: %d, %s", w.Code, w.Body)
	}
	action(url.Values{"action": {"delete"}, "path": {"guide.txt"}})
	if _, err := os.Stat(filepath.Join(srv.dir, "test-user", "guide.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted file remains: %v", err)
	}
}

func TestExplorerActionsRequireDialogConfirmation(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, _ := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "original.txt", "keep")
	for _, location := range []string{"/", "/files"} {
		r := httptest.NewRequest(http.MethodGet, location, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d, %s", location, w.Code, w.Body)
		}
		body := w.Body.String()
		for _, action := range []string{"rename", "move", "delete-file", "share"} {
			button := regexp.MustCompile(`<button\b[^>]*data-dialog-action="` + action + `"[^>]*>`).FindString(body)
			if button == "" || !strings.Contains(button, `type="button"`) || !strings.Contains(button, `data-path="original.txt"`) {
				t.Errorf("%s %s must open a dialog without submitting: %q", location, action, button)
			}
		}
		if location == "/" {
			if strings.Count(body, `<dialog id="actionDialog"`) != 1 {
				t.Error("explorer must render exactly one shared dialog")
			}
			for _, markup := range []string{`<dialog id="actionDialog"`, `<form id="actionDialogForm" method="post"`, `id="actionDialogCancel" type="button"`, `id="actionDialogValue"`, `id="actionDialogConfirmLabel">Save</span>`} {
				if !strings.Contains(body, markup) {
					t.Errorf("explorer is missing shared dialog control %s", markup)
				}
			}
		} else if strings.Contains(body, `<dialog`) {
			t.Error("file list refresh must reuse the page's shared dialog")
		}
	}
}

func TestFileActionsRejectUnsafePathsAndConflicts(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "folder/sub/a.txt", "original")
	writeStoredFile(t, srv, "test-user", "taken.txt", "taken")
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
		srv.handleFiles(w, formRequest("/files", tt.form, cookie))
		if w.Code != tt.status {
			t.Errorf("action %v: %d, %s; want %d", tt.form, w.Code, w.Body, tt.status)
		}
	}
	for _, name := range []string{"folder/sub/a.txt", "taken.txt"} {
		if _, err := os.Stat(filepath.Join(srv.dir, "test-user", filepath.FromSlash(name))); err != nil {
			t.Fatalf("rejected operation changed stored file: %v", err)
		}
	}
	// A same-name rename is harmless and can be retried.
	w := httptest.NewRecorder()
	srv.handleFiles(w, formRequest("/files", url.Values{"action": {"rename"}, "path": {"taken.txt"}, "name": {"taken.txt"}, "csrf_token": {csrf}}, cookie))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("same-name rename: %d, %s", w.Code, w.Body)
	}
}

func TestExplorerAccountIsolationAndCSRF(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	other, err := srv.accounts.Create(context.Background(), "Other User", testPassword, false, quota.DefaultQuota)
	if err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, srv, other.Slug, "private.txt", "secret")
	writeStoredFile(t, srv, "test-user", "visible.txt", "mine")
	cookie, csrf := sessionFor(t, srv, "Test User")
	handler := srv.Handler()
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
	if data, err := os.ReadFile(filepath.Join(srv.dir, "test-user", "visible.txt")); err != nil || string(data) != "mine" {
		t.Fatalf("rejected deletion changed file: %q, %v", data, err)
	}
}

func TestNewNamesRejectHiddenFormatCharactersAndOlderNamesStayAccessible(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 1000)
	cookie, csrf := sessionFor(t, srv, "Test User")
	handler := srv.Handler()
	rightToLeftOverride := string(rune(0x202E))
	spoofed := "invoice" + rightToLeftOverride + "fdp.exe" // Displays as invoiceexe.pdf.
	folder := "old" + rightToLeftOverride + "redlof"
	writeStoredFile(t, srv, "test-user", spoofed, "older upload")
	writeStoredFile(t, srv, "test-user", folder+"/inside.txt", "inside")
	writeStoredFile(t, srv, "test-user", "plain.txt", "plain")
	action := func(form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("csrf_token", csrf)
		return serve(handler, formRequest("/files", form, cookie))
	}
	get := func(target string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.AddCookie(cookie)
		return serve(handler, r)
	}

	for _, form := range []url.Values{
		{"action": {"mkdir"}, "name": {"new" + rightToLeftOverride + "folder"}},
		{"action": {"mkdir"}, "name": {"isolate" + string(rune(0x2067))}},
		{"action": {"mkdir"}, "name": {"zero" + string(rune(0x200B)) + "width"}},
		{"action": {"rename"}, "path": {"plain.txt"}, "name": {"plain" + rightToLeftOverride + "txt.exe"}},
		{"action": {"rename"}, "path": {spoofed}, "name": {spoofed + "2"}},
	} {
		if w := action(form); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invisible formatting characters") {
			t.Errorf("action %q: %d %s; want 400", form, w.Code, w.Body)
		}
	}
	for _, name := range []string{"نیم" + string(rune(0x200C)) + "فاصله", "👩" + string(rune(0x200D)) + "💻"} {
		if w := action(url.Values{"action": {"mkdir"}, "name": {name}}); w.Code != http.StatusSeeOther {
			t.Errorf("mkdir %q: %d %s", name, w.Code, w.Body)
		}
	}

	// Files stored before the rule are listed, downloaded under a cleaned name,
	// shared, and can be moved, renamed to a safe name, or deleted.
	if w := get("/files"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), spoofed) {
		t.Fatalf("listing older names: %d", w.Code)
	}
	w := get("/download?" + url.Values{"path": {spoofed}}.Encode())
	_, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if w.Code != http.StatusOK || w.Body.String() != "older upload" || params["filename"] != "invoicefdp.exe" || err != nil {
		t.Fatalf("download of an older name: %d, %q, %v", w.Code, w.Header().Get("Content-Disposition"), err)
	}
	w = get("/download?" + url.Values{"path": {folder}}.Encode())
	_, params, err = mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if w.Code != http.StatusOK || params["filename"] != "oldredlof.zip" || err != nil {
		t.Fatalf("folder download of an older name: %d, %q, %v", w.Code, w.Header().Get("Content-Disposition"), err)
	}
	archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range archive.File {
		if strings.ContainsRune(file.Name, 0x202E) {
			t.Errorf("folder archive entry keeps the override: %q", file.Name)
		}
	}
	if w := get("/shares?" + url.Values{"path": {spoofed}}.Encode()); w.Code != http.StatusOK {
		t.Fatalf("share lookup for an older name: %d %s", w.Code, w.Body)
	}
	for _, form := range []url.Values{
		{"action": {"mkdir"}, "dir": {folder}, "name": {"sub"}},
		{"action": {"move"}, "path": {spoofed}, "destination": {folder}},
		{"action": {"rename"}, "path": {folder + "/" + spoofed}, "name": {"invoice.exe"}},
		{"action": {"delete"}, "path": {folder}},
	} {
		if w := action(form); w.Code != http.StatusSeeOther {
			t.Errorf("action %q on an older name: %d %s", form, w.Code, w.Body)
		}
	}
}
