package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/storage"
)

func allowMethods(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

// serverError answers a request that failed on the server and logs err. A
// client that disconnects also makes server work fail; that is logged only for
// debugging.
func serverError(w http.ResponseWriter, r *http.Request, message string, err error) {
	if r.Context().Err() != nil {
		slog.Debug(message, "err", err)
	} else {
		slog.Error(message, "err", err)
	}
	http.Error(w, message, http.StatusInternalServerError)
}

func writeStorageError(w http.ResponseWriter, err error) {
	message, status := storageError(err)
	http.Error(w, message, status)
}

// storageError returns the message and status that answer err, and logs the
// failures it answers with a server error.
func storageError(err error) (string, int) {
	switch {
	case errors.Is(err, storage.ErrInvalidPath):
		return err.Error(), http.StatusBadRequest
	case errors.Is(err, storage.ErrConflict), errors.Is(err, os.ErrExist):
		return err.Error(), http.StatusConflict
	case errors.Is(err, quota.ErrExceeded):
		return "disk quota exceeded", http.StatusRequestEntityTooLarge
	case errors.Is(err, os.ErrNotExist):
		return "file or folder not found", http.StatusNotFound
	case errors.Is(err, account.ErrInvalidCredentials):
		return "unauthorized", http.StatusUnauthorized
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "operation canceled", http.StatusRequestTimeout
	default:
		slog.Error("unable to access files", "err", err)
		return "unable to access files", http.StatusInternalServerError
	}
}
