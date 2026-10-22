package pigeon

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

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

func writeStorageError(w http.ResponseWriter, err error) {
	message, status := storageError(err)
	http.Error(w, message, status)
}

func storageError(err error) (string, int) {
	switch {
	case errors.Is(err, storage.ErrInvalidPath):
		return err.Error(), http.StatusBadRequest
	case errors.Is(err, storage.ErrConflict), errors.Is(err, os.ErrExist):
		return err.Error(), http.StatusConflict
	case errors.Is(err, errDiskQuotaExceeded):
		return "disk quota exceeded", http.StatusRequestEntityTooLarge
	case errors.Is(err, os.ErrNotExist):
		return "file or folder not found", http.StatusNotFound
	case errors.Is(err, errInvalidCredentials):
		return "unauthorized", http.StatusUnauthorized
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "operation canceled", http.StatusRequestTimeout
	default:
		return "unable to access files", http.StatusInternalServerError
	}
}
