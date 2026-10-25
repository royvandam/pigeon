package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/royvandam/pigeon/pkg/quota"
)

const testUploadID = "0123456789abcdef0123456789abcdef"

func waitForUploadProgress(t *testing.T, uploads *uploadTracker, state string, minimumBytes int64) uploadProgressSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, ok := uploads.snapshot(uploadOwner{userID: 1}, testUploadID)
		if ok && snapshot.State == state && snapshot.Bytes >= minimumBytes {
			return snapshot
		}
		time.Sleep(time.Millisecond)
	}
	snapshot, ok := uploads.snapshot(uploadOwner{userID: 1}, testUploadID)
	t.Fatalf("progress = %#v, found=%t; want %s with at least %d bytes", snapshot, ok, state, minimumBytes)
	return uploadProgressSnapshot{}
}

func TestUploadProgressTracksServerWritesAndUserOwnership(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 1<<20)
	cookie, csrf := sessionFor(t, srv, "Test User")
	if _, err := srv.accounts.Create(context.Background(), "Other User", testPassword, false, quota.DefaultQuota); err != nil {
		t.Fatal(err)
	}
	otherCookie, _ := sessionFor(t, srv, "Other User")
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
		srv.handleUpload(w, r)
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
	snapshot := waitForUploadProgress(t, srv.uploads, "uploading", 1)
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
			srv.Handler().ServeHTTP(response, request)
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
	srv.handleUpload(response, duplicate)
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
	snapshot = waitForUploadProgress(t, srv.uploads, "complete", int64(len(first)+len(second)))
	if w.Code != http.StatusOK || snapshot.Bytes != int64(len(first)+len(second)) {
		t.Fatalf("final progress: status=%d, snapshot=%#v, response=%s", w.Code, snapshot, w.Body)
	}
}

func TestUploadProgressQueuesAndEndsOnCancellation(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 100)
	cookie, csrf := sessionFor(t, srv, "Test User")
	unlock, err := srv.locks.Lock(context.Background(), 1)
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
		srv.handleUpload(w, r)
		close(done)
	}()
	waitForUploadProgress(t, srv.uploads, "queued", 0)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled upload did not stop")
	}
	snapshot := waitForUploadProgress(t, srv.uploads, "failed", 0)
	if w.Code != http.StatusRequestTimeout || body.read || snapshot.Bytes != 0 {
		t.Fatalf("canceled queue: status=%d, body read=%t, progress=%#v", w.Code, body.read, snapshot)
	}
}

func TestUploadProgressQuotaFailureAndIDValidation(t *testing.T) {
	t.Parallel()
	// The folder takes one unit, leaving four bytes for file contents.
	srv := newTestServer(t, 5)
	r := uploadFilesRequest(t, uploadFile{"folder/a.txt", "123"}, uploadFile{"folder/b.txt", "456"})
	r.Header.Set("X-Upload-ID", testUploadID)
	w := httptest.NewRecorder()
	srv.handleUpload(w, r)
	snapshot := waitForUploadProgress(t, srv.uploads, "failed", 4)
	entries, err := readUploadDir(srv.dir, "test-user")
	if w.Code != http.StatusRequestEntityTooLarge || snapshot.Bytes != 4 || err != nil || len(entries) != 0 {
		t.Fatalf("failed batch: status=%d, progress=%#v, files=%v, err=%v", w.Code, snapshot, entries, err)
	}
	for _, id := range []string{"short", strings.Repeat("x", 65), "../../another-user"} {
		r := uploadRequest(t, "invalid.txt", "data")
		r.Header.Set("X-Upload-ID", id)
		body := &readTrackingBody{}
		r.Body = body
		w := httptest.NewRecorder()
		srv.handleUpload(w, r)
		if w.Code != http.StatusBadRequest || body.read {
			t.Fatalf("invalid ID %q: status=%d, body read=%t", id, w.Code, body.read)
		}
	}
}

func TestUploadProgressExpiration(t *testing.T) {
	t.Parallel()
	uploads := newUploadTracker()
	p, err := uploads.begin(uploadOwner{userID: 1}, testUploadID)
	if err != nil {
		t.Fatal(err)
	}
	p.Written(42)
	p.setState("complete")
	p.fail() // A successful upload must keep its final state.
	snapshot, ok := uploads.snapshot(uploadOwner{userID: 1}, testUploadID)
	if !ok || snapshot.State != "complete" || snapshot.Bytes != 42 {
		t.Fatalf("completed upload: %#v, %t", snapshot, ok)
	}
	p.mu.Lock()
	p.finished = time.Now().Add(-uploadProgressRetention - time.Second)
	p.mu.Unlock()
	if _, ok := uploads.snapshot(uploadOwner{userID: 1}, testUploadID); ok {
		t.Fatal("expired progress is still accessible")
	}
	if len(uploads.uploads) != 0 {
		t.Fatalf("expired progress was not removed: %v", uploads.uploads)
	}
	if _, err := uploads.begin(uploadOwner{userID: 1}, testUploadID); err != nil {
		t.Fatalf("expired ID is still reserved: %v", err)
	}
}

func TestUploadProgressIsBoundedPerOwner(t *testing.T) {
	t.Parallel()
	uploads := newUploadTracker()
	owner, other := uploadOwner{link: "link-hash"}, uploadOwner{userID: 1}
	id := func(i int) string { return fmt.Sprintf("%s%04d", testUploadID[:16], i) }
	var first *uploadProgress
	for i := range maxTrackedUploads {
		p, err := uploads.begin(owner, id(i))
		if err != nil {
			t.Fatalf("upload %d: %v", i, err)
		}
		if i == 0 {
			first = p
		}
	}
	// Uploads in progress are never dropped to make room.
	if _, err := uploads.begin(owner, id(maxTrackedUploads)); !errors.Is(err, errTooManyUploads) {
		t.Fatalf("upload beyond the limit: %v", err)
	}
	if _, err := uploads.begin(other, testUploadID); err != nil {
		t.Fatalf("another owner was limited: %v", err)
	}
	// A finished upload makes room, and its record goes first.
	first.setState("complete")
	if _, err := uploads.begin(owner, id(maxTrackedUploads)); err != nil {
		t.Fatal(err)
	}
	if _, ok := uploads.snapshot(owner, id(0)); ok {
		t.Fatal("the finished upload's record was kept")
	}
	if _, ok := uploads.snapshot(owner, id(1)); !ok {
		t.Fatal("an upload in progress lost its record")
	}

	// Expired records of owners who stopped uploading are swept periodically.
	for _, p := range uploads.uploads[owner] {
		p.fail()
		p.mu.Lock()
		p.finished = time.Now().Add(-uploadProgressRetention)
		p.mu.Unlock()
	}
	uploads.lastSweep = time.Now().Add(-uploadProgressRetention)
	if _, err := uploads.begin(other, id(1)); err != nil {
		t.Fatal(err)
	}
	if _, ok := uploads.uploads[owner]; ok || len(uploads.uploads) != 1 {
		t.Fatalf("expired records were not swept: %v", uploads.uploads)
	}
}

func TestCancelStopsAnUploadWhoseConnectionStaysOpen(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, 1<<20)
	cookie, csrf := sessionFor(t, srv, "Test User")
	server := httptest.NewServer(srv.Handler())
	defer server.Close()
	post := func(target, contentType string, body io.Reader, header http.Header) (*http.Response, error) {
		r, err := http.NewRequest(http.MethodPost, server.URL+target, body)
		if err != nil {
			return nil, err
		}
		r.Header = header.Clone()
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(cookie)
		return server.Client().Do(r)
	}

	// The first upload sends part of a file and then nothing, with its
	// connection open, as through a proxy that does not pass on a canceled
	// request.
	reader, writer := io.Pipe()
	defer writer.Close()
	mw := multipart.NewWriter(writer)
	go func() {
		part, err := mw.CreateFormFile("file", "stalled.bin")
		if err == nil {
			part.Write(make([]byte, 1000))
		}
	}()
	go post("/upload", mw.FormDataContentType(), reader, http.Header{"X-Upload-Id": {testUploadID}})
	waitForUploadProgress(t, srv.uploads, "uploading", 1000)

	// A second upload waits for the first to give up the upload slot.
	second := make(chan int, 1)
	go func() {
		var body strings.Builder
		smw := multipart.NewWriter(&body)
		part, _ := smw.CreateFormFile("file", "second.txt")
		io.WriteString(part, "second")
		smw.Close()
		response, err := post("/upload", smw.FormDataContentType(), strings.NewReader(body.String()), http.Header{})
		if err != nil {
			second <- 0
			return
		}
		response.Body.Close()
		second <- response.StatusCode
	}()
	select {
	case code := <-second:
		t.Fatalf("the second upload did not wait for the first: %d", code)
	case <-time.After(200 * time.Millisecond):
	}

	cancel := func() int {
		form := url.Values{"action": {"cancel"}, "csrf_token": {csrf}}
		response, err := post("/upload-progress?id="+testUploadID, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if code := cancel(); code != http.StatusNoContent {
		t.Fatalf("cancel: %d", code)
	}
	select {
	case code := <-second:
		if code != http.StatusOK {
			t.Fatalf("second upload after the cancel: %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second upload still waits after the cancel")
	}
	if _, err := os.Stat(filepath.Join(srv.dir, "test-user", "stalled.bin")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the canceled upload was saved: %v", err)
	}
	if snapshot, _ := srv.uploads.snapshot(uploadOwner{userID: 1}, testUploadID); snapshot.State != "canceled" {
		t.Fatalf("canceled upload state = %q", snapshot.State)
	}
	if code := cancel(); code != http.StatusNoContent {
		t.Fatalf("canceling a canceled upload again: %d", code)
	}
}

func TestCancelingAnUpload(t *testing.T) {
	t.Parallel()
	uploads := newUploadTracker()
	owner, other := uploadOwner{link: "link-hash"}, uploadOwner{userID: 1}

	// An upload in progress stops, once, and only for its own owner.
	p, err := uploads.begin(owner, testUploadID)
	if err != nil {
		t.Fatal(err)
	}
	stops := 0
	p.onStop(func() { stops++ })
	if canceled, err := uploads.cancel(owner, testUploadID); !canceled || err != nil || stops != 1 {
		t.Fatalf("cancel = %t, %v with %d stops", canceled, err, stops)
	}
	if canceled, err := uploads.cancel(owner, testUploadID); !canceled || err != nil || stops != 1 {
		t.Fatalf("second cancel = %t, %v with %d stops", canceled, err, stops)
	}
	p.fail()
	if snapshot, _ := uploads.snapshot(owner, testUploadID); snapshot.State != "canceled" {
		t.Fatalf("a stopped upload ended %q", snapshot.State)
	}
	if snapshot, ok := uploads.snapshot(other, testUploadID); ok {
		t.Fatalf("another owner sees the upload: %+v", snapshot)
	}

	// A cancel before the upload arrives, as through a buffering proxy, is kept,
	// and the upload is refused when it comes.
	early := testUploadID[:30] + "ee"
	if canceled, err := uploads.cancel(other, early); !canceled || err != nil {
		t.Fatalf("early cancel = %t, %v", canceled, err)
	}
	if snapshot, _ := uploads.snapshot(other, early); snapshot.State != "canceled" {
		t.Fatalf("early cancel state %q", snapshot.State)
	}
	if _, err := uploads.begin(other, early); !errors.Is(err, errUploadCanceled) {
		t.Fatalf("an upload canceled before it arrived began: %v", err)
	}

	// A cancel during the moment before the handler can stop the upload takes
	// effect as soon as it can.
	pending := testUploadID[:30] + "pp"
	p, err = uploads.begin(owner, pending)
	if err != nil {
		t.Fatal(err)
	}
	if canceled, err := uploads.cancel(owner, pending); !canceled || err != nil {
		t.Fatalf("cancel before onStop = %t, %v", canceled, err)
	}
	stops = 0
	p.onStop(func() { stops++ })
	if stops != 1 {
		t.Fatal("onStop did not stop an upload canceled before it")
	}

	// A completed upload cannot be canceled: its files are saved.
	done := testUploadID[:30] + "dd"
	p, err = uploads.begin(owner, done)
	if err != nil {
		t.Fatal(err)
	}
	p.onStop(func() { t.Fatal("stopped a completed upload") })
	p.setState("complete")
	if canceled, err := uploads.cancel(owner, done); canceled || err != nil {
		t.Fatalf("cancel after completion = %t, %v", canceled, err)
	}

	// Kept cancels count toward the limit, and give way to uploads in progress.
	full := uploadOwner{link: "full"}
	for i := range maxTrackedUploads {
		if _, err := uploads.begin(full, fmt.Sprintf("%s%04d", testUploadID[:16], i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := uploads.cancel(full, early); !errors.Is(err, errTooManyUploads) {
		t.Fatalf("cancel with no room: %v", err)
	}
}
