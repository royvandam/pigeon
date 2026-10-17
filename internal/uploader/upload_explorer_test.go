package uploader

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUploadMergesFoldersAndRejectsExistingFiles(t *testing.T) {
	cfg := newTestConfig(t, 100)
	writeStoredFile(t, cfg, "test-user", "destination/project/existing.txt", "keep")
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
		cfg.handleUpload(w, r)
		if w.Code != tt.status {
			t.Fatalf("upload %v: %d, %s; want %d", tt.files, w.Code, w.Body, tt.status)
		}
	}
	for name, contents := range map[string]string{"project/existing.txt": "keep", "project/docs/new.txt": "new", "original ?.txt": "name"} {
		data, err := os.ReadFile(filepath.Join(cfg.dir, "test-user", "destination", filepath.FromSlash(name)))
		if err != nil || string(data) != contents {
			t.Fatalf("stored %s: %q, %v; want %q", name, data, err, contents)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.dir, "test-user", "destination", "never-published")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed upload published a partial folder: %v", err)
	}
	entries, err := readUploadDir(cfg.dir, "test-user")
	if err != nil || len(entries) != 1 || entries[0].Name() != "destination" {
		t.Fatalf("unexpected wrapper or staging folder: %v, %v", entries, err)
	}
}

func TestUploadRejectsInvalidDestinationsBeforeReadingBody(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "file.txt", "file")
	for destination, status := range map[string]int{"../other-user": 400, "/absolute": 400, "folder/../other": 400, ".upload-private": 400, "missing": 404, "file.txt": 409} {
		r := httptest.NewRequest(http.MethodPost, "/upload?"+url.Values{"dir": {destination}}.Encode(), nil)
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
		body := &readTrackingBody{}
		r.Body = body
		w := httptest.NewRecorder()
		cfg.handleUpload(w, r)
		if w.Code != status || body.read {
			t.Errorf("destination %q: %d, body read=%t; want %d without reading", destination, w.Code, body.read, status)
		}
	}
}

func TestUploadPublicationRollbackPreservesExistingFolders(t *testing.T) {
	cfg := newTestConfig(t, 100)
	writeStoredFile(t, cfg, "test-user", "destination/project/existing.txt", "keep")
	batch, err := newUploadBatch(filepath.Join(cfg.dir, "test-user"), "destination")
	if err != nil {
		t.Fatal(err)
	}
	defer batch.close()
	for _, name := range []string{"project/new/a.txt", "other/b.txt"} {
		if err := batch.checkPath(name); err != nil {
			t.Fatal(err)
		}
		if err := batch.add(name, strings.NewReader("data"), 100); err != nil {
			t.Fatal(err)
		}
	}
	// Fail after the first file has been published and a second parent created.
	if err := batch.stage.Remove("other/b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := batch.publish(); err == nil {
		t.Fatal("publication succeeded with missing staged file")
	}
	for _, name := range []string{"project/new", "other"} {
		if _, err := os.Stat(filepath.Join(cfg.dir, "test-user", "destination", filepath.FromSlash(name))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("rollback left %s behind: %v", name, err)
		}
	}
	used, err := diskUsage(context.Background(), filepath.Join(cfg.dir, "test-user", "destination"))
	if err != nil || used != 4 {
		t.Fatalf("rollback changed existing data: used=%d, %v", used, err)
	}
}

type gatedUploadBody struct {
	io.ReadCloser
	started chan struct{}
	release chan struct{}
}

func (b *gatedUploadBody) Read(p []byte) (int, error) {
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	<-b.release
	return b.ReadCloser.Read(p)
}

func TestFileActionsWaitForActiveUpload(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "destination/keep.txt", "keep")
	r := uploadFilesRequest(t, uploadFile{"new.txt", "new"})
	r.URL.RawQuery = "dir=destination"
	r.Header.Del("Authorization")
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	body := &gatedUploadBody{ReadCloser: r.Body, started: make(chan struct{}), release: make(chan struct{})}
	r.Body = body
	done := make(chan struct{})
	w := httptest.NewRecorder()
	go func() {
		cfg.handleUpload(w, r)
		close(done)
	}()
	defer func() {
		close(body.release)
		<-done
	}()
	select {
	case <-body.started:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not start reading")
	}
	// The upload holds the user's mutation lock while receiving data. Removing
	// its destination must wait; canceling that wait must leave the folder intact.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	form := url.Values{"action": {"delete"}, "path": {"destination"}, "csrf_token": {csrf}}
	mutation := httptest.NewRecorder()
	cfg.handleFiles(mutation, formRequest("/files", form, cookie).WithContext(ctx))
	if mutation.Code != http.StatusRequestTimeout {
		t.Fatalf("file mutation bypassed active upload: %d, %s", mutation.Code, mutation.Body)
	}
	if _, err := os.Stat(filepath.Join(cfg.dir, "test-user", "destination", "keep.txt")); err != nil {
		t.Fatalf("queued deletion changed destination: %v", err)
	}
}
