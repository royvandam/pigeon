package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// linkUploadRequest uploads files through the upload link at link, without
// signing in.
func linkUploadRequest(t *testing.T, link string, files ...uploadFile) *http.Request {
	t.Helper()
	r := uploadFilesRequest(t, files...)
	r.URL.Path, r.RequestURI = link, link
	r.Header.Del("Authorization")
	return r
}

func TestUploadLinksAcceptFilesWithoutShowingTheFolder(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 1000)
	cookie, csrf := sessionFor(t, srv, "Test User")
	writeStoredFile(t, srv, "test-user", "inbox/secret-report.pdf", "old")

	before := time.Now()
	code, created := manageLink(t, srv, cookie, csrf, url.Values{"action": {"create"}, "path": {"inbox"}, "kind": {"upload"}, "lifetime": {"24h"}})
	if code != http.StatusOK || !strings.HasPrefix(created.URL, "/u/") || !created.Active || created.Kind != "upload" ||
		created.ExpiresAt == nil || created.ExpiresAt.Before(before.Add(24*time.Hour-time.Second)) {
		t.Fatalf("create upload link: %d, %+v", code, created)
	}
	link := created.URL
	if _, status := manageLink(t, srv, cookie, csrf, url.Values{"path": {"inbox"}, "kind": {"upload"}}); !status.Active || status.URL != "" || status.ExpiresAt == nil {
		t.Fatalf("upload link status = %+v", status)
	}
	if _, status := manageLink(t, srv, cookie, csrf, url.Values{"path": {"inbox"}}); status.Active || status.Kind != "download" {
		t.Fatalf("download link status = %+v", status)
	}

	// The page names the folder, but not what it holds.
	w := publicDownload(srv, http.MethodGet, link)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Upload to") || !strings.Contains(w.Body.String(), "inbox") ||
		strings.Contains(w.Body.String(), "secret-report") || !strings.Contains(w.Body.String(), "This link works until") {
		t.Fatalf("upload page: %d, %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("X-Robots-Tag") == "" {
		t.Fatalf("upload page headers: %v", w.Header())
	}
	// An upload link does not download, and a download link does not upload.
	if w := publicDownload(srv, http.MethodGet, "/s/"+strings.TrimPrefix(link, "/u/")); w.Code != http.StatusNotFound {
		t.Fatalf("downloaded through an upload link: %d", w.Code)
	}

	r := linkUploadRequest(t, link, uploadFile{"secret-report.pdf", "new"}, uploadFile{"Photos/a.jpg", "a"})
	r.Header.Set("X-Upload-ID", testUploadID)
	w = serve(srv.Handler(), r)
	if w.Code != http.StatusOK || uploadResult(t, w, "saved_as") != "." || uploadResult(t, w, "files") != "2" {
		t.Fatalf("upload through a link: %d, %s", w.Code, w.Body)
	}
	if w.Header().Get("X-Quota-Remaining") != "" || w.Header().Get("X-Quota-Used") != "" {
		t.Fatalf("an upload through a link revealed the owner's storage: %v", w.Header())
	}
	for name, want := range map[string]string{"secret-report.pdf": "old", "secret-report (2).pdf": "new", "Photos/a.jpg": "a"} {
		if got, err := os.ReadFile(filepath.Join(srv.dir, "test-user", "inbox", filepath.FromSlash(name))); err != nil || string(got) != want {
			t.Errorf("inbox/%s = %q, %v; want %q", name, got, err, want)
		}
	}
	// Progress belongs to the link, not to the owner's own uploads.
	r = httptest.NewRequest(http.MethodGet, link+"/progress?id="+testUploadID, nil)
	if w := serve(srv.Handler(), r); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"complete"`) {
		t.Fatalf("link upload progress: %d, %s", w.Code, w.Body)
	}
	r = httptest.NewRequest(http.MethodGet, "/upload-progress?id="+testUploadID, nil)
	r.AddCookie(cookie)
	if w := serve(srv.Handler(), r); w.Code != http.StatusNotFound {
		t.Fatalf("the owner sees a link upload's progress: %d", w.Code)
	}

	// Another site cannot post through the link from a visitor's browser.
	r = linkUploadRequest(t, link, uploadFile{"x.txt", "x"})
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if w := serve(srv.Handler(), r); w.Code != http.StatusForbidden {
		t.Fatalf("cross-site upload: %d, %s", w.Code, w.Body)
	}

	// Revoking the upload link stops the page, uploads, and progress.
	if code, revoked := manageLink(t, srv, cookie, csrf, url.Values{"action": {"revoke"}, "path": {"inbox"}, "kind": {"upload"}}); code != http.StatusOK || revoked.Active {
		t.Fatalf("revoke: %d, %+v", code, revoked)
	}
	if w := publicDownload(srv, http.MethodGet, link); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "no longer works") {
		t.Fatalf("revoked upload page: %d, %s", w.Code, w.Body)
	}
	if w := serve(srv.Handler(), linkUploadRequest(t, link, uploadFile{"late.txt", "x"})); w.Code != http.StatusNotFound {
		t.Fatalf("upload through a revoked link: %d, %s", w.Code, w.Body)
	}
	if w := serve(srv.Handler(), httptest.NewRequest(http.MethodGet, link+"/progress?id="+testUploadID, nil)); w.Code != http.StatusNotFound {
		t.Fatalf("progress through a revoked link: %d", w.Code)
	}
}

func TestUploadLinksHideTheOwnersQuota(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	if err := os.MkdirAll(filepath.Join(srv.dir, "test-user", "inbox"), 0o750); err != nil {
		t.Fatal(err)
	}
	_, created := manageLink(t, srv, cookie, csrf, url.Values{"action": {"create"}, "path": {"inbox"}, "kind": {"upload"}})
	if created.ExpiresAt != nil {
		t.Fatalf("a link without a lifetime expires: %+v", created)
	}
	w := serve(srv.Handler(), linkUploadRequest(t, created.URL, uploadFile{"big.bin", strings.Repeat("x", 200)}))
	if w.Code != http.StatusRequestEntityTooLarge || strings.Contains(w.Body.String(), "available") {
		t.Fatalf("over-quota upload through a link: %d, %s", w.Code, w.Body)
	}
}

func TestInvalidUploadLinksKeepNoState(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	for i := range 8 {
		secret := strings.Repeat(string(rune('A'+i)), 43)
		r := httptest.NewRequest(http.MethodPost, "/u/"+secret, strings.NewReader(""))
		r.Header.Set("Content-Type", "multipart/form-data; boundary=review")
		r.Header.Set("X-Upload-ID", testUploadID[:30]+strings.Repeat(string(rune('a'+i)), 2))
		if w := serve(srv.Handler(), r); w.Code != http.StatusNotFound {
			t.Fatalf("upload through an invalid link: %d, %s", w.Code, w.Body)
		}
	}
	srv.uploads.mu.Lock()
	defer srv.uploads.mu.Unlock()
	if len(srv.uploads.uploads) != 0 {
		t.Fatalf("invalid links left progress records: %v", srv.uploads.uploads)
	}
}
