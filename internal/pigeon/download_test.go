package pigeon

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileDownloadAttachmentRangeAndHead(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, _ := sessionFor(t, cfg, "Test User")
	filename := `résumé "<tag>".html`
	writeStoredFile(t, cfg, "test-user", filename, "<html>private</html>")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequest(method, "/download?"+url.Values{"path": {filename}}.Encode(), nil)
		r.AddCookie(cookie)
		r.Header.Set("Range", "bytes=0-5")
		w := httptest.NewRecorder()
		cfg.handleDownload(w, r)
		kind, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
		if w.Code != http.StatusPartialContent || kind != "attachment" || params["filename"] != filename || err != nil || w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("download %s: %d, %v, %v", method, w.Code, w.Header(), err)
		}
		if method == http.MethodGet && w.Body.String() != "<html>" || method == http.MethodHead && w.Body.Len() != 0 {
			t.Fatalf("download %s body=%q", method, w.Body)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/files", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	cfg.handleFiles(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "<tag>") || !strings.Contains(w.Body.String(), "&lt;tag&gt;") {
		t.Fatalf("filename was not escaped in explorer: %d, %s", w.Code, w.Body)
	}
}

func TestDirectoryDownloadIncludesHierarchyAndEmptyFolders(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, _ := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", "project/readme.txt", "root")
	writeStoredFile(t, cfg, "test-user", "project/写真/été.txt", "nested")
	writeStoredFile(t, cfg, "test-user", "project/.config", "dotfile")
	writeStoredFile(t, cfg, "test-user", ".upload-in-progress/partial.txt", "private stage")
	if err := os.MkdirAll(filepath.Join(cfg.dir, "test-user", "project", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"project", ""} {
		r := httptest.NewRequest(http.MethodGet, "/download?"+url.Values{"path": {name}}.Encode(), nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		cfg.handleDownload(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" {
			t.Fatalf("directory download %q: %d, %s", name, w.Code, w.Body)
		}
		archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		contents := make(map[string]string)
		for _, file := range archive.File {
			if strings.Contains(file.Name, stagingPrefix) {
				t.Fatalf("private staging files leaked: %s", file.Name)
			}
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			contents[file.Name] = string(data)
		}
		prefix := ""
		if name == "" {
			prefix = "test-user/"
		}
		for entry, want := range map[string]string{"project/readme.txt": "root", "project/写真/été.txt": "nested", "project/.config": "dotfile", "project/empty/": ""} {
			got, exists := contents[prefix+entry]
			if !exists || got != want {
				t.Errorf("ZIP entry %s = %q, exists=%t; want %q", prefix+entry, got, exists, want)
			}
		}
	}
}

func TestPrivateStagingAndSymlinksCannotBeAccessed(t *testing.T) {
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	writeStoredFile(t, cfg, "test-user", ".upload-active/secret.txt", "in progress")
	writeStoredFile(t, cfg, "test-user", "20261010T102812Z_old/project/a.txt", "legacy upload")
	r := httptest.NewRequest(http.MethodGet, "/files", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	cfg.handleFiles(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), ".upload-active") || !strings.Contains(w.Body.String(), "20261010T102812Z_old") {
		t.Fatalf("listing should hide staging and retain legacy folders: %d, %s", w.Code, w.Body)
	}
	other, err := cfg.store.createUser(context.Background(), "Other User", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, cfg, other.Slug, "secret.txt", "other user")
	if err := os.Symlink(filepath.Join(cfg.dir, other.Slug), filepath.Join(cfg.dir, "test-user", "link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".upload-active", ".upload-active/secret.txt", "link", "link/secret.txt"} {
		for _, endpoint := range []string{"/files?dir=", "/download?path="} {
			r := httptest.NewRequest(http.MethodGet, endpoint+url.QueryEscape(name), nil)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			newServer(cfg).Handler.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("private path %s: %d, %s", endpoint+name, w.Code, w.Body)
			}
		}
		for _, action := range []string{"delete", "rename", "move"} {
			form := url.Values{"action": {action}, "path": {name}, "name": {"renamed"}, "csrf_token": {csrf}}
			w := httptest.NewRecorder()
			cfg.handleFiles(w, formRequest("/files", form, cookie))
			if w.Code != http.StatusBadRequest {
				t.Errorf("private path %s %s: %d", action, name, w.Code)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(cfg.dir, other.Slug, "secret.txt")); err != nil || string(data) != "other user" {
		t.Fatalf("other user's data was changed: %q, %v", data, err)
	}
}
