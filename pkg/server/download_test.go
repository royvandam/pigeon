package server

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/storage"
)

func TestFileDownloadAttachmentRangeAndHead(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, _ := sessionFor(t, srv, "Test User")
	filename := `résumé "<tag>".html`
	writeStoredFile(t, srv, "test-user", filename, "<html>private</html>")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequest(method, "/download?"+url.Values{"path": {filename}}.Encode(), nil)
		r.AddCookie(cookie)
		r.Header.Set("Range", "bytes=0-5")
		w := httptest.NewRecorder()
		srv.handleDownload(w, r)
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
	srv.handleFiles(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "<tag>") || !strings.Contains(w.Body.String(), "&lt;tag&gt;") {
		t.Fatalf("filename was not escaped in explorer: %d, %s", w.Code, w.Body)
	}
}

func TestDirectoryDownloadIncludesHierarchyAndEmptyFolders(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, _ := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "project/readme.txt", "root")
	writeStoredFile(t, srv, "test-user", "project/写真/été.txt", "nested")
	writeStoredFile(t, srv, "test-user", "project/.config", "dotfile")
	writeStoredFile(t, srv, "test-user", ".upload-in-progress/partial.txt", "private stage")
	if err := os.MkdirAll(filepath.Join(srv.dir, "test-user", "project", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"project", ""} {
		r := httptest.NewRequest(http.MethodGet, "/download?"+url.Values{"path": {name}}.Encode(), nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		srv.handleDownload(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" {
			t.Fatalf("directory download %q: %d, %s", name, w.Code, w.Body)
		}
		archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		contents := make(map[string]string)
		for _, file := range archive.File {
			if strings.Contains(file.Name, storage.ReservedPrefix) {
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
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", ".upload-active/secret.txt", "in progress")
	writeStoredFile(t, srv, "test-user", "archive/project/a.txt", "archived upload")
	r := httptest.NewRequest(http.MethodGet, "/files", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	srv.handleFiles(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), ".upload-active") || !strings.Contains(w.Body.String(), "archive") {
		t.Fatalf("listing should hide staging and show other folders: %d, %s", w.Code, w.Body)
	}
	other, err := srv.accounts.Create(context.Background(), "Other User", testPassword, false, quota.DefaultQuota)
	if err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, srv, other.Slug, "secret.txt", "other user")
	if err := os.Symlink(filepath.Join(srv.dir, other.Slug), filepath.Join(srv.dir, "test-user", "link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".upload-active", ".upload-active/secret.txt", "link", "link/secret.txt"} {
		for _, endpoint := range []string{"/files?dir=", "/download?path="} {
			r := httptest.NewRequest(http.MethodGet, endpoint+url.QueryEscape(name), nil)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("private path %s: %d, %s", endpoint+name, w.Code, w.Body)
			}
		}
		for _, action := range []string{"delete", "rename", "move"} {
			form := url.Values{"action": {action}, "path": {name}, "name": {"renamed"}, "csrf_token": {csrf}}
			w := httptest.NewRecorder()
			srv.handleFiles(w, formRequest("/files", form, cookie))
			if w.Code != http.StatusBadRequest {
				t.Errorf("private path %s %s: %d", action, name, w.Code)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(srv.dir, other.Slug, "secret.txt")); err != nil || string(data) != "other user" {
		t.Fatalf("other user's data was changed: %q, %v", data, err)
	}
}

// zipContents reads every file in a ZIP archive.
func zipContents(t *testing.T, body []byte) map[string]string {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	contents := make(map[string]string)
	for _, file := range archive.File {
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
	return contents
}

// Names are checked as paths in the account, where only the first element can
// look like a Windows drive. A folder download must accept the same names
// inside the folder.
func TestFolderDownloadsIncludeEveryUploadedName(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 1000)
	cookie, csrf := sessionFor(t, srv, "Test User")
	handler := srv.Handler()
	upload := uploadFilesRequest(t, uploadFile{"project/C:notes.txt", "payload"}, uploadFile{"project/z.txt", "later file"})
	if w := serve(handler, upload); w.Code != http.StatusOK {
		t.Fatalf("upload: %d, %s", w.Code, w.Body)
	}
	link := shareRequest(t, srv, cookie, csrf, "create", "project")
	private := httptest.NewRequest(http.MethodGet, "/download?path=project", nil)
	private.AddCookie(cookie)
	for name, r := range map[string]*http.Request{
		"private download": private,
		"share link":       httptest.NewRequest(http.MethodGet, link, nil),
	} {
		w := serve(handler, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d, %s", name, w.Code, w.Body)
		}
		contents := zipContents(t, w.Body.Bytes())
		for entry, want := range map[string]string{"project/C:notes.txt": "payload", "project/z.txt": "later file"} {
			if got, exists := contents[entry]; !exists || got != want {
				t.Errorf("%s: ZIP entry %s = %q, exists=%t; want %q", name, entry, got, exists, want)
			}
		}
	}
}

// failingWriter is a response whose connection broke.
type failingWriter struct{ *httptest.ResponseRecorder }

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// This test replaces the global log output, so it must not call t.Parallel.
func TestFolderDownloadLogsAnErrorOnlyWhenTheClientRemains(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	srv := newTestServer(t, 100)
	ctx := context.Background()
	u, err := srv.accounts.Authenticate(ctx, "Test User", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, srv, u.Slug, "project/readme.txt", "root")
	download := func(ctx context.Context) string {
		d, err := srv.openPrivateDownload(context.Background(), u, "project")
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		output.Reset()
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/download?path=project", nil)
		serveDownload(failingWriter{httptest.NewRecorder()}, r, d)
		return output.String()
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if line := download(canceled); line != "" {
		t.Errorf("a download the client abandoned logged %q", line)
	}
	if line := download(ctx); !strings.Contains(line, "ERROR folder download failed") {
		t.Errorf("a failed download logged %q; want an error", line)
	}
}
