package pigeon

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/royvandam/pigeon/pkg/quota"
)

const testUploadID = "0123456789abcdef0123456789abcdef"

func waitForUploadProgress(t *testing.T, s *store, state string, minimumBytes int64) uploadProgressSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, ok := s.uploadProgress(1, testUploadID)
		if ok && snapshot.State == state && snapshot.Bytes >= minimumBytes {
			return snapshot
		}
		time.Sleep(time.Millisecond)
	}
	snapshot, ok := s.uploadProgress(1, testUploadID)
	t.Fatalf("progress = %#v, found=%t; want %s with at least %d bytes", snapshot, ok, state, minimumBytes)
	return uploadProgressSnapshot{}
}

func TestUploadProgressTracksServerWritesAndUserOwnership(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 1<<20)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	if _, err := cfg.store.accounts.Create(context.Background(), "Other User", testPassword, false, quota.DefaultQuota); err != nil {
		t.Fatal(err)
	}
	otherCookie, _ := sessionFor(t, cfg, "Other User")
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	mw := multipart.NewWriter(writer)
	r := httptest.NewRequest(http.MethodPost, "/upload", reader)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("X-Upload-ID", testUploadID)
	r.Header.Set("X-CSRF-Token", csrf)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		cfg.handleUpload(w, r)
		close(done)
	}()
	part, err := mw.CreateFormFile("file", "folder/first.txt")
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Repeat("x", 64<<10)
	if _, err := io.WriteString(part, first); err != nil {
		t.Fatal(err)
	}
	snapshot := waitForUploadProgress(t, cfg.store, "uploading", 1)
	if snapshot.Bytes > int64(len(first)) {
		t.Fatalf("progress counted more than the streamed file: %#v", snapshot)
	}
	select {
	case <-done:
		t.Fatal("upload completed while the request was still streaming")
	default:
	}
	for _, tt := range []struct {
		name   string
		cookie *http.Cookie
		status int
	}{
		{"owner", cookie, http.StatusOK},
		{"another user", otherCookie, http.StatusNotFound},
		{"anonymous", nil, http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/upload-progress?id="+testUploadID, nil)
			if tt.cookie != nil {
				request.AddCookie(tt.cookie)
			}
			response := httptest.NewRecorder()
			newServer(cfg).Handler.ServeHTTP(response, request)
			if response.Code != tt.status || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("progress response: %d, %v, %s", response.Code, response.Header(), response.Body)
			}
			if tt.status == http.StatusOK {
				var got uploadProgressSnapshot
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got.State != "uploading" || got.Bytes == 0 || got.Bytes > int64(len(first)) {
					t.Fatalf("progress response = %#v, %v", got, err)
				}
			}
		})
	}
	// A second request cannot replace an active upload's progress record.
	duplicate := uploadRequest(t, "duplicate.txt", "data")
	duplicate.Header.Del("Authorization")
	duplicate.AddCookie(cookie)
	duplicate.Header.Set("X-CSRF-Token", csrf)
	duplicate.Header.Set("X-Upload-ID", testUploadID)
	body := &readTrackingBody{}
	duplicate.Body = body
	response := httptest.NewRecorder()
	cfg.handleUpload(response, duplicate)
	if response.Code != http.StatusConflict || body.read {
		t.Fatalf("duplicate progress ID: status=%d, body read=%t", response.Code, body.read)
	}
	part, err = mw.CreateFormFile("file", "folder/sub/second.txt")
	if err != nil {
		t.Fatal(err)
	}
	second := "second file"
	if _, err := io.WriteString(part, second); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("note", "multipart metadata must not count toward progress"); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not finish")
	}
	snapshot = waitForUploadProgress(t, cfg.store, "complete", int64(len(first)+len(second)))
	if w.Code != http.StatusOK || snapshot.Bytes != int64(len(first)+len(second)) {
		t.Fatalf("final progress: status=%d, snapshot=%#v, response=%s", w.Code, snapshot, w.Body)
	}
}

func TestUploadProgressQueuesAndEndsOnCancellation(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t, 100)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	unlock, err := cfg.locks.Lock(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	r := uploadRequest(t, "queued.txt", "data")
	r.Header.Del("Authorization")
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("X-Upload-ID", testUploadID)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	body := &readTrackingBody{}
	r.Body = body
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		cfg.handleUpload(w, r)
		close(done)
	}()
	waitForUploadProgress(t, cfg.store, "queued", 0)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled upload did not stop")
	}
	snapshot := waitForUploadProgress(t, cfg.store, "failed", 0)
	if w.Code != http.StatusRequestTimeout || body.read || snapshot.Bytes != 0 {
		t.Fatalf("canceled queue: status=%d, body read=%t, progress=%#v", w.Code, body.read, snapshot)
	}
}

func TestUploadProgressQuotaFailureAndIDValidation(t *testing.T) {
	t.Parallel()
	// The folder takes one unit, leaving four bytes for file contents.
	cfg := newTestConfig(t, 5)
	r := uploadFilesRequest(t, uploadFile{"folder/a.txt", "123"}, uploadFile{"folder/b.txt", "456"})
	r.Header.Set("X-Upload-ID", testUploadID)
	w := httptest.NewRecorder()
	cfg.handleUpload(w, r)
	snapshot := waitForUploadProgress(t, cfg.store, "failed", 4)
	entries, err := readUploadDir(cfg.dir, "test-user")
	if w.Code != http.StatusRequestEntityTooLarge || snapshot.Bytes != 4 || err != nil || len(entries) != 0 {
		t.Fatalf("failed batch: status=%d, progress=%#v, files=%v, err=%v", w.Code, snapshot, entries, err)
	}
	for _, id := range []string{"short", strings.Repeat("x", 65), "../../another-user"} {
		r := uploadRequest(t, "invalid.txt", "data")
		r.Header.Set("X-Upload-ID", id)
		body := &readTrackingBody{}
		r.Body = body
		w := httptest.NewRecorder()
		cfg.handleUpload(w, r)
		if w.Code != http.StatusBadRequest || body.read {
			t.Fatalf("invalid ID %q: status=%d, body read=%t", id, w.Code, body.read)
		}
	}
}

func TestUploadProgressExpiration(t *testing.T) {
	t.Parallel()
	var s store
	p, err := s.beginUploadProgress(1, testUploadID)
	if err != nil {
		t.Fatal(err)
	}
	p.Written(42)
	p.setState("complete")
	p.fail() // A successful upload must keep its final state.
	snapshot, ok := s.uploadProgress(1, testUploadID)
	if !ok || snapshot.State != "complete" || snapshot.Bytes != 42 {
		t.Fatalf("completed upload: %#v, %t", snapshot, ok)
	}
	p.mu.Lock()
	p.finished = time.Now().Add(-uploadProgressRetention - time.Second)
	p.mu.Unlock()
	if _, ok := s.uploadProgress(1, testUploadID); ok {
		t.Fatal("expired progress is still accessible")
	}
	if _, ok := s.progress.Load(uploadProgressKey{1, testUploadID}); ok {
		t.Fatal("expired progress was not removed")
	}
	if _, err := s.beginUploadProgress(1, testUploadID); err != nil {
		t.Fatalf("expired ID is still reserved: %v", err)
	}
}
