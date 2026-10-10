package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func uploadRequest(t *testing.T, filename, contents string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("note", "metadata before the file"); err != nil {
		t.Fatal(err)
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, contents); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.SetBasicAuth("Test User", testPassword)
	return r
}

func TestUploadSizeBoundaryAndCleanup(t *testing.T) {
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
			cfg := newTestConfig(t, 4)
			w := httptest.NewRecorder()
			cfg.handleUpload(w, uploadRequest(t, "../../report?.txt", tt.contents))
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body)
			}
			files, err := readUploadDir(cfg.dir, "test-user")
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
			if !strings.HasSuffix(name, "_report_.txt") {
				t.Fatalf("unexpected sanitized filename: %q", name)
			}
			got, err := os.ReadFile(filepath.Join(cfg.dir, "test-user", name))
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

type readTrackingBody struct {
	read bool
}

func (b *readTrackingBody) Read([]byte) (int, error) {
	b.read = true
	return 0, errors.New("unexpected body read")
}

func (*readTrackingBody) Close() error { return nil }

func TestUploadAuthenticatesBeforeReadingBody(t *testing.T) {
	cfg := newTestConfig(t, 4)
	body := &readTrackingBody{}
	r := httptest.NewRequest(http.MethodPost, "/upload", nil)
	r.Body = body
	r.SetBasicAuth("Test User", "wrong")
	w := httptest.NewRecorder()
	cfg.handleUpload(w, r)
	if w.Code != http.StatusUnauthorized || body.read {
		t.Fatalf("status = %d, body read = %t; want 401 without reading", w.Code, body.read)
	}
}

func TestUploadRejectsInvalidMultipart(t *testing.T) {
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
			cfg := newTestConfig(t, 100)
			r := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", tt.contentType)
			r.SetBasicAuth("Test User", testPassword)
			w := httptest.NewRecorder()
			cfg.handleUpload(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body)
			}
			files, err := readUploadDir(cfg.dir, "test-user")
			if err != nil || len(files) != 0 {
				t.Fatalf("failed upload left files: %v, %v", files, err)
			}
		})
	}
}

func TestUploadCapsFieldsBeforeFile(t *testing.T) {
	cfg := newTestConfig(t, 4)
	body := io.MultiReader(
		strings.NewReader("--test\r\nContent-Disposition: form-data; name=\"note\"\r\n\r\n"),
		strings.NewReader(strings.Repeat("x", int(multipartOverhead)+1024)),
		strings.NewReader("\r\n--test--\r\n"),
	)
	r := httptest.NewRequest(http.MethodPost, "/upload", body)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	r.SetBasicAuth("Test User", testPassword)
	w := httptest.NewRecorder()
	cfg.handleUpload(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", w.Code, w.Body)
	}
}

func TestUploadsAreIsolatedAndPreservedOnDeletion(t *testing.T) {
	cfg := newTestConfig(t, 100)
	u, err := cfg.store.createUser(context.Background(), "Other User", testPassword, false)
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
		cfg.handleUpload(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("upload for %s: %d %s", tt.username, w.Code, w.Body)
		}
		files, err := readUploadDir(cfg.dir, tt.slug)
		if err != nil || len(files) != 1 {
			t.Fatalf("upload folder for %s: %v, %v", tt.username, files, err)
		}
		data, err := os.ReadFile(filepath.Join(cfg.dir, tt.slug, files[0].Name()))
		if err != nil || string(data) != tt.contents {
			t.Fatalf("wrong user's data saved: %q, %v", data, err)
		}
	}
	if err := cfg.store.deleteUser(context.Background(), u.ID, 999); err != nil {
		t.Fatal(err)
	}
	files, err := readUploadDir(cfg.dir, "other-user")
	if err != nil || len(files) != 1 {
		t.Fatalf("deletion removed existing uploads: %v, %v", files, err)
	}
}

func TestSharedPasswordHeaderNoLongerAuthenticates(t *testing.T) {
	cfg := newTestConfig(t, 100)
	body := &readTrackingBody{}
	r := httptest.NewRequest(http.MethodPost, "/upload", nil)
	r.Body = body
	r.Header.Set("X-Upload-Password", testPassword)
	w := httptest.NewRecorder()
	cfg.handleUpload(w, r)
	if w.Code != http.StatusUnauthorized || body.read {
		t.Fatalf("legacy password header accepted: status %d, body read %t", w.Code, body.read)
	}
}
