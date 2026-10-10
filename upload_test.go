package main

import (
	"bytes"
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
	r.Header.Set("X-Upload-Password", "secret")
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
			cfg := config{dir: t.TempDir(), maxBytes: 4, password: "secret"}
			w := httptest.NewRecorder()
			cfg.handleUpload(w, uploadRequest(t, "../../report?.txt", tt.contents))
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body)
			}
			files, err := os.ReadDir(cfg.dir)
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
			got, err := os.ReadFile(filepath.Join(cfg.dir, name))
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
	cfg := config{dir: t.TempDir(), maxBytes: 4, password: "secret"}
	body := &readTrackingBody{}
	r := httptest.NewRequest(http.MethodPost, "/upload", nil)
	r.Body = body
	r.Header.Set("X-Upload-Password", "wrong")
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
			cfg := config{dir: t.TempDir(), maxBytes: 100, password: "secret"}
			r := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", tt.contentType)
			r.Header.Set("X-Upload-Password", "secret")
			w := httptest.NewRecorder()
			cfg.handleUpload(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body)
			}
			files, err := os.ReadDir(cfg.dir)
			if err != nil || len(files) != 0 {
				t.Fatalf("failed upload left files: %v, %v", files, err)
			}
		})
	}
}

func TestUploadCapsFieldsBeforeFile(t *testing.T) {
	cfg := config{dir: t.TempDir(), maxBytes: 4, password: "secret"}
	body := io.MultiReader(
		strings.NewReader("--test\r\nContent-Disposition: form-data; name=\"note\"\r\n\r\n"),
		strings.NewReader(strings.Repeat("x", int(multipartOverhead)+1024)),
		strings.NewReader("\r\n--test--\r\n"),
	)
	r := httptest.NewRequest(http.MethodPost, "/upload", body)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	r.Header.Set("X-Upload-Password", "secret")
	w := httptest.NewRecorder()
	cfg.handleUpload(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", w.Code, w.Body)
	}
}
