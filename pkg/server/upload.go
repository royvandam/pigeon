package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"

	"github.com/royvandam/pigeon/internal/size"
	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/files"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/storage"
)

// multipartOverhead allows for the framing of an upload beyond its files.
const multipartOverhead = quota.Headroom

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}

	// Authenticate and check the session CSRF header before touching the body.
	a, ok := s.requireAuth(w, r, authOptions{})
	if !ok {
		return
	}

	// Parse multipart manually so we can stream.
	ct := r.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || mediaType != "multipart/form-data" {
		http.Error(w, "expected multipart form", http.StatusBadRequest)
		return
	}

	boundary := params["boundary"]
	if boundary == "" {
		http.Error(w, "missing multipart boundary", http.StatusBadRequest)
		return
	}
	progress, err := s.uploads.begin(a.User.ID, r.Header.Get("X-Upload-ID"))
	if err != nil {
		status := http.StatusConflict
		if !uploadIDPattern.MatchString(r.Header.Get("X-Upload-ID")) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	defer progress.fail()

	// The upload holds the account lock only to check the quota and to
	// publish, so the account's pages, share links, and file actions do not
	// wait for the body. Its slot keeps this account's uploads streaming one
	// at a time.
	upload, err := s.files.BeginUpload(r.Context(), a.User, progress)
	if err != nil {
		http.Error(w, "upload canceled", http.StatusRequestTimeout)
		return
	}
	defer upload.Close()
	usage, err := upload.Prepare(r.Context(), r.URL.Query().Get("dir"))
	if usage.Known() {
		setQuotaHeaders(w, usage)
	}
	var quotaErr *files.QuotaError
	if errors.As(err, &quotaErr) {
		http.Error(w, "disk quota exhausted", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		writeUploadError(w, err)
		return
	}

	// The deadline stays in place on failure: the server then reads the rest of
	// a small body before responding, and must not wait for a stalled client.
	body := newIdleTimeoutReader(w, r.Body, s.uploadTimeout)
	r.Body = http.MaxBytesReader(w, body, usage.Remaining()+multipartOverhead)
	defer r.Body.Close()
	mr := multipart.NewReader(r.Body, boundary)
	progress.setState("uploading")

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "multipart read error", uploadReadErrorStatus(err))
			return
		}

		if part.FormName() != "file" {
			if _, err := io.Copy(io.Discard, part); err != nil {
				http.Error(w, "multipart read error", uploadReadErrorStatus(err))
				return
			}
			part.Close()
			continue
		}

		// Part.FileName strips directories; read the original filename parameter
		// so browser webkitRelativePath values retain their hierarchy.
		_, disposition, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil {
			http.Error(w, "invalid file disposition", http.StatusBadRequest)
			return
		}
		if err := upload.Add(r.Context(), disposition["filename"], part); err != nil {
			var fileErr *os.PathError
			switch {
			case errors.As(err, &quotaErr):
				http.Error(w, fmt.Sprintf("disk quota exceeded (%s available)", size.Format(quotaErr.Available)), http.StatusRequestEntityTooLarge)
			case errors.Is(err, storage.ErrInvalidPath), errors.Is(err, storage.ErrConflict):
				writeStorageError(w, err)
			case errors.As(err, &fileErr):
				http.Error(w, "upload failed", http.StatusInternalServerError)
			default:
				http.Error(w, "upload failed", uploadReadErrorStatus(err))
			}
			return
		}
		part.Close()
	}

	if upload.Files() == 0 {
		http.Error(w, "missing file", http.StatusBadRequest)
		return
	}
	body.clear()
	if r.Context().Err() != nil {
		http.Error(w, "upload canceled", http.StatusRequestTimeout)
		return
	}
	progress.setState("finalizing")
	receipt, err := upload.Publish(r.Context())
	if receipt.Usage.Known() {
		setQuotaHeaders(w, receipt.Usage)
	}
	if err != nil {
		writeUploadError(w, err)
		return
	}
	progress.setState("complete")

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "ok\nsaved_as=%s\nbytes=%d\n", receipt.SavedAs, receipt.Bytes)
	if receipt.Group {
		fmt.Fprintf(w, "files=%d\n", receipt.Files)
	}
}

// writeUploadError answers an upload that could not check the account or
// publish its files.
func writeUploadError(w http.ResponseWriter, err error) {
	var quotaErr *files.QuotaError
	switch {
	case errors.Is(err, account.ErrInvalidCredentials):
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	case errors.Is(err, files.ErrUsageUnavailable):
		http.Error(w, "failed to check storage usage", http.StatusInternalServerError)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "upload canceled", http.StatusRequestTimeout)
	case errors.Is(err, files.ErrQuotaUnavailable):
		http.Error(w, "failed to load disk quota", http.StatusInternalServerError)
	case errors.As(err, &quotaErr):
		http.Error(w, fmt.Sprintf("disk quota exceeded (%s available)", size.Format(quotaErr.Available)), http.StatusRequestEntityTooLarge)
	default:
		writeStorageError(w, err)
	}
}

func uploadReadErrorStatus(err error) int {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return http.StatusRequestEntityTooLarge
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return http.StatusRequestTimeout
	}
	return http.StatusBadRequest
}
