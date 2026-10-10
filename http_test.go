package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestStorageErrorResponses(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
		text   string
	}{
		{fmt.Errorf("%w: malformed name", errStoragePath), 400, "malformed name"},
		{errFileConflict, 409, "already exists"},
		{os.ErrNotExist, 404, "not found"},
		{errInvalidCredentials, 401, "unauthorized"},
		{context.Canceled, 408, "operation canceled"},
		{context.DeadlineExceeded, 408, "operation canceled"},
		{errors.New("internal filesystem detail"), 500, "unable to access files"},
	} {
		w := httptest.NewRecorder()
		writeStorageError(w, tt.err)
		if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.text) || strings.Contains(w.Body.String(), "internal filesystem detail") {
			t.Fatalf("storage error %v: %d %s", tt.err, w.Code, w.Body)
		}
	}
}

func TestRoutesRejectUnsupportedMethodsConsistently(t *testing.T) {
	cfg := newTestConfig(t, 100)
	for route, allow := range map[string]string{
		"/": "GET", "/files": "GET, POST", "/admin": "GET, POST",
		"/login": "GET, POST", "/logout": "POST", "/upload": "POST",
		"/upload-progress": "GET", "/shares": "GET, POST",
		"/download": "GET, HEAD", "/s/token": "GET, HEAD", "/assets/dialog.js": "GET, HEAD",
	} {
		w := httptest.NewRecorder()
		newServer(cfg).Handler.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, route, nil))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != allow {
			t.Fatalf("DELETE %s: %d Allow=%q", route, w.Code, w.Header().Get("Allow"))
		}
	}
}
