package main

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type uploadFile struct {
	name     string
	contents string
}

func uploadFilesRequest(t *testing.T, files ...uploadFile) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, file := range files {
		part, err := mw.CreateFormFile("file", file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, file.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.SetBasicAuth("Test User", testPassword)
	return r
}

func uploadResult(t *testing.T, w *httptest.ResponseRecorder, key string) string {
	t.Helper()
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if value, ok := strings.CutPrefix(line, key+"="); ok {
			return value
		}
	}
	t.Fatalf("missing %s in upload response: %s", key, w.Body)
	return ""
}

func TestDirectoryUploadPreservesHierarchyAndNames(t *testing.T) {
	files := []uploadFile{
		{"My project/readme.txt", "root"},
		{"My project/docs/readme.txt", "nested"},
		{"My project/.config/settings.json", "{}"},
		{"My project/写真/été ?.txt", "unicode"},
		{"My project/run 10:30/output.txt", "colon"},
		{"My project/empty.txt", ""},
	}
	var total int
	for _, file := range files {
		total += len(file.contents)
	}
	cfg := newTestConfig(t, int64(total*2))
	cookie, csrf := sessionFor(t, cfg, "Test User")
	for attempt := range 2 {
		r := uploadFilesRequest(t, files...)
		r.Header.Del("Authorization")
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		cfg.handleUpload(w, r)
		if attempt == 1 {
			if w.Code != http.StatusConflict {
				t.Fatalf("repeat upload should preserve existing files: %d, %s", w.Code, w.Body)
			}
			continue
		}
		if w.Code != http.StatusOK || uploadResult(t, w, "files") != strconv.Itoa(len(files)) || uploadResult(t, w, "bytes") != strconv.Itoa(total) {
			t.Fatalf("directory upload: %d, %s", w.Code, w.Body)
		}
		name := uploadResult(t, w, "saved_as")
		if name != "." {
			t.Fatalf("upload added a wrapper folder: %q", name)
		}
		for _, file := range files {
			data, err := os.ReadFile(filepath.Join(cfg.dir, "test-user", name, filepath.FromSlash(file.name)))
			if err != nil || string(data) != file.contents {
				t.Fatalf("saved %s = %q, %v; want %q", file.name, data, err, file.contents)
			}
		}
	}
	entries, err := readUploadDir(cfg.dir, "test-user")
	used, usageErr := diskUsage(context.Background(), filepath.Join(cfg.dir, "test-user"))
	if err != nil || usageErr != nil || len(entries) != 1 || entries[0].Name() != "My project" || used != int64(total) {
		t.Fatalf("completed batches: entries=%v, used=%d, errors=%v/%v", entries, used, err, usageErr)
	}
}

func TestUploadMultipleFilesAndSingleFileDirectory(t *testing.T) {
	for _, files := range [][]uploadFile{
		{{"first.txt", "one"}, {"second.txt", "two"}},
		{{"project/subdir/only.txt", "lone file"}},
		{{"project/a.txt", "1234"}, {"project/empty.txt", ""}},
	} {
		quota := int64(100)
		if len(files) == 2 && files[1].contents == "" {
			quota = 4
		}
		cfg := newTestConfig(t, quota)
		w := httptest.NewRecorder()
		cfg.handleUpload(w, uploadFilesRequest(t, files...))
		if w.Code != http.StatusOK || uploadResult(t, w, "files") != strconv.Itoa(len(files)) {
			t.Fatalf("batch upload: %d, %s", w.Code, w.Body)
		}
		name := uploadResult(t, w, "saved_as")
		for _, file := range files {
			data, err := os.ReadFile(filepath.Join(cfg.dir, "test-user", name, filepath.FromSlash(file.name)))
			if err != nil || string(data) != file.contents {
				t.Fatalf("saved %s = %q, %v", file.name, data, err)
			}
		}
	}
}

func TestDirectoryQuotaFailureRemovesWholeBatch(t *testing.T) {
	cfg := newTestConfig(t, 10)
	w := httptest.NewRecorder()
	cfg.handleUpload(w, uploadRequest(t, "existing.txt", "1234"))
	if w.Code != http.StatusOK {
		t.Fatalf("existing upload: %d, %s", w.Code, w.Body)
	}
	existing := uploadResult(t, w, "saved_as")
	w = httptest.NewRecorder()
	cfg.handleUpload(w, uploadFilesRequest(t, uploadFile{"folder/a.txt", "123"}, uploadFile{"folder/b.txt", "4567"}))
	if w.Code != http.StatusRequestEntityTooLarge || w.Header().Get("X-Quota-Used") != "4" || w.Header().Get("X-Quota-Remaining") != "6" {
		t.Fatalf("combined quota check: %d, %v, %s", w.Code, w.Header(), w.Body)
	}
	entries, err := readUploadDir(cfg.dir, "test-user")
	if err != nil || len(entries) != 1 || entries[0].Name() != existing {
		t.Fatalf("failed batch changed existing uploads or left partial data: %v, %v", entries, err)
	}
	w = httptest.NewRecorder()
	cfg.handleUpload(w, uploadFilesRequest(t, uploadFile{"folder/a.txt", "123"}, uploadFile{"folder/b.txt", "456"}))
	if w.Code != http.StatusOK || w.Header().Get("X-Quota-Remaining") != "0" {
		t.Fatalf("exact remaining quota: %d, %s", w.Code, w.Body)
	}
}

func TestDirectoryRejectsUnsafeAndConflictingPaths(t *testing.T) {
	for _, name := range []string{
		"../outside.txt", "folder/../../outside.txt", "/absolute.txt",
		`folder\..\outside.txt`, "C:/outside.txt", "C:outside.txt", "folder//file.txt",
		"folder/./file.txt", "folder/../file.txt", "folder/", ".", "..", "",
		"folder/" + strings.Repeat("a", 256),
	} {
		t.Run(name, func(t *testing.T) {
			cfg := newTestConfig(t, 100)
			w := httptest.NewRecorder()
			cfg.handleUpload(w, uploadFilesRequest(t, uploadFile{"folder/valid.txt", "data"}, uploadFile{name, "bad"}))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("unsafe path %q accepted: %d, %s", name, w.Code, w.Body)
			}
			entries, err := readUploadDir(cfg.dir, "test-user")
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed batch left data: %v, %v", entries, err)
			}
		})
	}
	for _, names := range [][2]string{
		{"folder/a.txt", "folder/a.txt"},
		{"folder/a", "folder/a/b.txt"},
		{"folder/a/b.txt", "folder/a"},
	} {
		cfg := newTestConfig(t, 100)
		w := httptest.NewRecorder()
		cfg.handleUpload(w, uploadFilesRequest(t, uploadFile{names[0], "first"}, uploadFile{names[1], "second"}))
		entries, err := readUploadDir(cfg.dir, "test-user")
		if w.Code != http.StatusBadRequest || err != nil || len(entries) != 0 {
			t.Fatalf("conflicting paths %v: status=%d, data=%v, err=%v", names, w.Code, entries, err)
		}
	}
}

func TestDirectoryTruncatedMultipartRemovesEarlierFiles(t *testing.T) {
	for _, tail := range []string{
		"--test\r\nContent-Disposition: form-data; name=\"file\"; filename=\"folder/b.txt\"\r\n\r\ntruncated",
		"--test\r\nContent-Disposition: form-data; name=\"note\"\r\n\r\ntruncated",
		"",
	} {
		cfg := newTestConfig(t, 100)
		body := "--test\r\nContent-Disposition: form-data; name=\"file\"; filename=\"folder/a.txt\"\r\n\r\ncomplete\r\n" + tail
		r := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
		r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
		r.SetBasicAuth("Test User", testPassword)
		w := httptest.NewRecorder()
		cfg.handleUpload(w, r)
		entries, err := readUploadDir(cfg.dir, "test-user")
		if w.Code != http.StatusBadRequest || err != nil || len(entries) != 0 {
			t.Fatalf("truncated batch: status=%d, data=%v, err=%v", w.Code, entries, err)
		}
	}
}

func TestDirectoryCapsFieldsAfterFiles(t *testing.T) {
	cfg := newTestConfig(t, 4)
	body := io.MultiReader(
		strings.NewReader("--test\r\nContent-Disposition: form-data; name=\"file\"; filename=\"folder/a.txt\"\r\n\r\nabc\r\n--test\r\nContent-Disposition: form-data; name=\"note\"\r\n\r\n"),
		strings.NewReader(strings.Repeat("x", int(multipartOverhead)+1024)),
		strings.NewReader("\r\n--test--\r\n"),
	)
	r := httptest.NewRequest(http.MethodPost, "/upload", body)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	r.SetBasicAuth("Test User", testPassword)
	w := httptest.NewRecorder()
	cfg.handleUpload(w, r)
	entries, err := readUploadDir(cfg.dir, "test-user")
	if w.Code != http.StatusRequestEntityTooLarge || err != nil || len(entries) != 0 {
		t.Fatalf("oversized trailing field: status=%d, data=%v, err=%v", w.Code, entries, err)
	}
}

type cancelingUploadBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelingUploadBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	b.cancel()
	return n, err
}

func TestDirectoryCancellationDoesNotPublishBatch(t *testing.T) {
	cfg := newTestConfig(t, 100)
	r := uploadFilesRequest(t, uploadFile{"folder/a.txt", "first"}, uploadFile{"folder/b.txt", "second"})
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	r.Body = cancelingUploadBody{ReadCloser: r.Body, cancel: cancel}
	w := httptest.NewRecorder()
	cfg.handleUpload(w, r)
	entries, err := readUploadDir(cfg.dir, "test-user")
	if w.Code != http.StatusRequestTimeout || err != nil || len(entries) != 0 {
		t.Fatalf("canceled batch: status=%d, data=%v, err=%v", w.Code, entries, err)
	}
}

func TestConcurrentDirectoryUploadsShareQuota(t *testing.T) {
	cfg := newTestConfig(t, 10)
	cookie, csrf := sessionFor(t, cfg, "Test User")
	const count = 4
	start := make(chan struct{})
	results := make(chan int, count)
	var workers sync.WaitGroup
	for i := range count {
		folder := "folder-" + strconv.Itoa(i)
		r := uploadFilesRequest(t, uploadFile{folder + "/a.txt", "123"}, uploadFile{folder + "/b.txt", "456"})
		r.Header.Del("Authorization")
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", csrf)
		workers.Go(func() {
			<-start
			w := httptest.NewRecorder()
			cfg.handleUpload(w, r)
			results <- w.Code
		})
	}
	close(start)
	workers.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == http.StatusOK {
			success++
		} else if status != http.StatusRequestEntityTooLarge {
			t.Fatalf("unexpected status %d", status)
		}
	}
	entries, err := readUploadDir(cfg.dir, "test-user")
	used, usageErr := diskUsage(context.Background(), filepath.Join(cfg.dir, "test-user"))
	if success != 1 || len(entries) != 1 || used != 6 || err != nil || usageErr != nil {
		t.Fatalf("concurrent batches exceeded quota or left data: success=%d, entries=%v, used=%d, errors=%v/%v", success, entries, used, err, usageErr)
	}
}
