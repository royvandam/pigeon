package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/royvandam/pigeon/pkg/account"
)

func TestUploadMergesFoldersAndRejectsExistingFiles(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	writeStoredFile(t, srv, "test-user", "destination/project/existing.txt", "keep")
	for _, tt := range []struct {
		files  []uploadFile
		status int
	}{
		{[]uploadFile{{"project/docs/new.txt", "new"}, {"original ?.txt", "name"}}, 200},
		{[]uploadFile{{"never-published/a.txt", "discard"}, {"project/existing.txt", "overwrite"}}, 409},
		{[]uploadFile{{"project", "file instead of folder"}}, 409},
		{[]uploadFile{{"original ?.txt/nested.txt", "folder instead of file"}}, 409},
	} {
		r := uploadFilesRequest(t, tt.files...)
		r.URL.RawQuery = url.Values{"dir": {"destination"}}.Encode()
		w := httptest.NewRecorder()
		srv.handleUpload(w, r)
		if w.Code != tt.status {
			t.Fatalf("upload %v: %d, %s; want %d", tt.files, w.Code, w.Body, tt.status)
		}
	}
	for name, contents := range map[string]string{"project/existing.txt": "keep", "project/docs/new.txt": "new", "original ?.txt": "name"} {
		data, err := os.ReadFile(filepath.Join(srv.dir, "test-user", "destination", filepath.FromSlash(name)))
		if err != nil || string(data) != contents {
			t.Fatalf("stored %s: %q, %v; want %q", name, data, err, contents)
		}
	}
	if _, err := os.Stat(filepath.Join(srv.dir, "test-user", "destination", "never-published")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed upload published a partial folder: %v", err)
	}
	entries, err := readUploadDir(srv.dir, "test-user")
	if err != nil || len(entries) != 1 || entries[0].Name() != "destination" {
		t.Fatalf("unexpected wrapper or staging folder: %v, %v", entries, err)
	}
}

func TestUploadRejectsInvalidDestinationsBeforeReadingBody(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "file.txt", "file")
	for destination, status := range map[string]int{"../other-user": 400, "/absolute": 400, "folder/../other": 400, ".upload-private": 400, "missing": 404, "file.txt": 409} {
		r := httptest.NewRequest(http.MethodPost, "/upload?"+url.Values{"dir": {destination}}.Encode(), nil)
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
		body := &readTrackingBody{}
		r.Body = body
		w := httptest.NewRecorder()
		srv.handleUpload(w, r)
		if w.Code != status || body.read {
			t.Errorf("destination %q: %d, body read=%t; want %d without reading", destination, w.Code, body.read, status)
		}
	}
}

func TestAccountRequestsDoNotWaitForUploadBodies(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	handler := srv.Handler()
	writeStoredFile(t, srv, "test-user", "destination/keep.txt", "keep")
	writeStoredFile(t, srv, "test-user", "shared.txt", "shared")
	link := shareRequest(t, srv, cookie, csrf, "create", "shared.txt")
	finish := startGatedUpload(t, srv, cookie, csrf, "destination", uploadFile{"new.txt", "new"})

	// While the body is pending, the account's pages, share links, and file
	// actions proceed instead of waiting for the upload.
	within := func(r *http.Request) *httptest.ResponseRecorder {
		t.Helper()
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		return serve(handler, r.WithContext(ctx))
	}
	page := httptest.NewRequest(http.MethodGet, "/", nil)
	page.AddCookie(cookie)
	if w := within(page); w.Code != http.StatusOK {
		t.Fatalf("explorer during upload: %d", w.Code)
	}
	if w := within(httptest.NewRequest(http.MethodGet, link, nil)); w.Code != http.StatusOK || w.Body.String() != "shared" {
		t.Fatalf("share link during upload: %d", w.Code)
	}
	form := url.Values{"action": {"delete"}, "path": {"destination"}, "csrf_token": {csrf}}
	if w := within(formRequest("/files", form, cookie)); w.Code != http.StatusSeeOther {
		t.Fatalf("deleting the upload's destination during upload: %d, %s", w.Code, w.Body)
	}

	// Publishing rechecks the destination, so the upload fails without
	// recreating the deleted folder or leaving its staging folder behind.
	if w := finish(); w.Code != http.StatusNotFound {
		t.Fatalf("upload into a deleted folder: %d, %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(srv.dir, "test-user", "destination")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("upload recreated its deleted destination: %v", err)
	}
	if staging := stagingFolders(t, srv); len(staging) != 0 {
		t.Fatalf("upload left staging folders: %v", staging)
	}
}

func TestUploadsRecheckAccountAndQuotaBeforePublishing(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		change func(t *testing.T, srv *testServer, u account.User, cookie *http.Cookie, csrf string)
		status int
	}{
		{"quota lowered", func(t *testing.T, srv *testServer, u account.User, _ *http.Cookie, _ string) {
			if err := srv.accounts.SetQuota(context.Background(), u.ID, 5); err != nil {
				t.Fatal(err)
			}
		}, http.StatusRequestEntityTooLarge},
		{"folders created", func(t *testing.T, srv *testServer, _ account.User, cookie *http.Cookie, csrf string) {
			for _, name := range []string{"a", "b"} {
				form := url.Values{"action": {"mkdir"}, "name": {name}, "csrf_token": {csrf}}
				if w := serve(srv.Handler(), formRequest("/files", form, cookie)); w.Code != http.StatusSeeOther {
					t.Fatalf("mkdir during upload: %d, %s", w.Code, w.Body)
				}
			}
		}, http.StatusRequestEntityTooLarge},
		{"account deleted", func(t *testing.T, srv *testServer, u account.User, _ *http.Cookie, _ string) {
			if err := srv.accounts.Delete(context.Background(), u.ID, 999); err != nil {
				t.Fatal(err)
			}
		}, http.StatusUnauthorized},
		{"nothing changed", func(*testing.T, *testServer, account.User, *http.Cookie, string) {}, http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Nine bytes fit the quota of ten when the upload starts.
			srv := newTestServer(t, 10)
			cookie, csrf := sessionFor(t, srv, "Test User")
			u, err := srv.accounts.Authenticate(context.Background(), "Test User", testPassword)
			if err != nil {
				t.Fatal(err)
			}
			finish := startGatedUpload(t, srv, cookie, csrf, "", uploadFile{"new.txt", "123456789"})
			tt.change(t, srv, u, cookie, csrf)
			if w := finish(); w.Code != tt.status {
				t.Fatalf("upload: %d, %s; want %d", w.Code, w.Body, tt.status)
			}
			_, err = os.Stat(filepath.Join(srv.dir, "test-user", "new.txt"))
			if published := err == nil; published != (tt.status == http.StatusOK) {
				t.Fatalf("published = %v after %d", published, tt.status)
			}
			if staging := stagingFolders(t, srv); len(staging) != 0 {
				t.Fatalf("upload left staging folders: %v", staging)
			}
		})
	}
}
