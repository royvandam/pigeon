package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/royvandam/pigeon/pkg/quota"
)

func TestUploadSizeBoundaryAndCleanup(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		contents   string
		wantStatus int
	}{
		{"empty", "", http.StatusOK},
		{"below limit", "abc", http.StatusOK},
		{"at limit", "abcd", http.StatusOK},
		{"above limit", "abcde", http.StatusRequestEntityTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, 4)
			w := httptest.NewRecorder()
			srv.handleUpload(w, uploadRequest(t, "report?.txt", tt.contents))
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body)
			}
			files, err := readUploadDir(srv.dir, "test-user")
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantStatus != http.StatusOK {
				if len(files) != 0 {
					t.Fatalf("failed upload left %d files", len(files))
				}
				return
			}
			if len(files) != 1 {
				t.Fatalf("saved %d files, want 1", len(files))
			}
			name := files[0].Name()
			if name != "report?.txt" {
				t.Fatalf("original filename was not preserved: %q", name)
			}
			got, err := os.ReadFile(filepath.Join(srv.dir, "test-user", name))
			if err != nil || string(got) != tt.contents {
				t.Fatalf("saved contents = %q, %v; want %q", got, err, tt.contents)
			}
			want := "ok\nsaved_as=" + name + "\nbytes=" + strconv.Itoa(len(tt.contents)) + "\n"
			if w.Body.String() != want {
				t.Fatalf("response = %q, want %q", w.Body.String(), want)
			}
		})
	}
}

func TestUploadAuthenticatesBeforeReadingBody(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 4)
	body := &readTrackingBody{}
	r := httptest.NewRequest(http.MethodPost, "/upload", nil)
	r.Body = body
	r.SetBasicAuth("Test User", "wrong")
	w := httptest.NewRecorder()
	srv.handleUpload(w, r)
	if w.Code != http.StatusUnauthorized || body.read {
		t.Fatalf("status = %d, body read = %t; want 401 without reading", w.Code, body.read)
	}
}

func TestUploadRejectsInvalidMultipart(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name        string
		contentType string
		body        string
	}{
		{"wrong media type", "multipart/mixed; boundary=test", "--test--\r\n"},
		{"missing boundary", "multipart/form-data", ""},
		{"missing file", "multipart/form-data; boundary=test", "--test--\r\n"},
		{"truncated file", "multipart/form-data; boundary=test", "--test\r\nContent-Disposition: form-data; name=\"file\"; filename=\"report.txt\"\r\n\r\npartial"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, 100)
			r := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", tt.contentType)
			r.SetBasicAuth("Test User", testPassword)
			w := httptest.NewRecorder()
			srv.handleUpload(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body)
			}
			files, err := readUploadDir(srv.dir, "test-user")
			if err != nil || len(files) != 0 {
				t.Fatalf("failed upload left files: %v, %v", files, err)
			}
		})
	}
}

func TestUploadCapsFieldsBeforeFile(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 4)
	body := io.MultiReader(
		strings.NewReader("--test\r\nContent-Disposition: form-data; name=\"note\"\r\n\r\n"),
		strings.NewReader(strings.Repeat("x", int(multipartOverhead)+1024)),
		strings.NewReader("\r\n--test--\r\n"),
	)
	r := httptest.NewRequest(http.MethodPost, "/upload", body)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	r.SetBasicAuth("Test User", testPassword)
	w := httptest.NewRecorder()
	srv.handleUpload(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", w.Code, w.Body)
	}
}

func TestUploadsAreIsolatedAndPreservedOnDeletion(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	u, err := srv.accounts.Create(context.Background(), "Other User", testPassword, false, quota.DefaultQuota)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ username, slug, contents string }{
		{"Test User", "test-user", "first user's data"},
		{"Other User", "other-user", "second user's data"},
	} {
		r := uploadRequest(t, "same-name.txt", tt.contents)
		r.SetBasicAuth(tt.username, testPassword)
		w := httptest.NewRecorder()
		srv.handleUpload(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("upload for %s: %d %s", tt.username, w.Code, w.Body)
		}
		files, err := readUploadDir(srv.dir, tt.slug)
		if err != nil || len(files) != 1 {
			t.Fatalf("upload folder for %s: %v, %v", tt.username, files, err)
		}
		data, err := os.ReadFile(filepath.Join(srv.dir, tt.slug, files[0].Name()))
		if err != nil || string(data) != tt.contents {
			t.Fatalf("wrong user's data saved: %q, %v", data, err)
		}
	}
	if err := srv.accounts.Delete(context.Background(), u.ID, 999); err != nil {
		t.Fatal(err)
	}
	files, err := readUploadDir(srv.dir, "other-user")
	if err != nil || len(files) != 1 {
		t.Fatalf("deletion removed existing uploads: %v, %v", files, err)
	}
}

func TestSharedPasswordHeaderNoLongerAuthenticates(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	body := &readTrackingBody{}
	r := httptest.NewRequest(http.MethodPost, "/upload", nil)
	r.Body = body
	r.Header.Set("X-Upload-Password", testPassword)
	w := httptest.NewRecorder()
	srv.handleUpload(w, r)
	if w.Code != http.StatusUnauthorized || body.read {
		t.Fatalf("legacy password header accepted: status %d, body read %t", w.Code, body.read)
	}
}

func TestUploadsRejectHiddenFormatCharactersInNewNames(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 1000)
	rightToLeftOverride := string(rune(0x202E))
	writeStoredFile(t, srv, "test-user", "old"+rightToLeftOverride+"txt.folder/kept.txt", "older upload")
	upload := func(filename, directory string) *httptest.ResponseRecorder {
		t.Helper()
		r := uploadRequest(t, filename, "data")
		r.URL.RawQuery = url.Values{"dir": {directory}}.Encode()
		w := httptest.NewRecorder()
		srv.handleUpload(w, r)
		return w
	}
	for _, name := range []string{
		"invoice" + rightToLeftOverride + "fdp.exe",
		"folder" + string(rune(0x2066)) + "/file.txt", // Left-to-right isolate.
		"report" + string(rune(0x200B)) + ".pdf",      // Zero-width space.
		string(rune(0xFEFF)) + "notes.txt",            // Byte order mark.
		"tag" + string(rune(0xE0041)) + ".txt",        // Tag character.
	} {
		if w := upload(name, ""); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invisible formatting characters") {
			t.Errorf("upload %q: %d %s; want 400", name, w.Code, w.Body)
		}
	}
	files, err := readUploadDir(srv.dir, "test-user")
	if err != nil || len(files) != 1 {
		t.Fatalf("rejected uploads stored files: %v, %v", files, err)
	}

	// Scripts and emoji that need zero-width (non-)joiners still work, as do
	// uploads into a folder created before the rule existed.
	persian := "نیم" + string(rune(0x200C)) + "فاصله.txt"
	family := "👨" + string(rune(0x200D)) + "👩" + string(rune(0x200D)) + "👧.txt"
	for _, tt := range []struct{ name, directory string }{
		{persian, ""},
		{family, ""},
		{"new.txt", "old" + rightToLeftOverride + "txt.folder"},
	} {
		if w := upload(tt.name, tt.directory); w.Code != http.StatusOK {
			t.Errorf("upload %q into %q: %d %s", tt.name, tt.directory, w.Code, w.Body)
		}
		if _, err := os.Stat(filepath.Join(srv.dir, "test-user", tt.directory, tt.name)); err != nil {
			t.Errorf("upload %q was not stored: %v", tt.name, err)
		}
	}
}
