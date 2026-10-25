package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"

	"github.com/royvandam/pigeon/internal/size"
	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/account"
	"github.com/royvandam/pigeon/pkg/files"
	"github.com/royvandam/pigeon/pkg/quota"
	"github.com/royvandam/pigeon/pkg/share"
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
	boundary, ok := multipartBoundary(w, r)
	if !ok {
		return
	}
	progress, ok := s.beginProgress(w, r, uploadOwner{userID: a.User.ID})
	if !ok {
		return
	}
	defer progress.fail()
	r, deadline, stop := stoppable(w, r, progress)
	defer stop()

	// The upload holds the account lock only to check the quota and to
	// publish, so the account's pages, share links, and file actions do not
	// wait for the body. Its slot keeps this account's uploads streaming one
	// at a time.
	upload, err := s.files.BeginUpload(r.Context(), a.User, r.URL.Query().Get("dir"), progress)
	if err != nil {
		http.Error(w, "upload canceled", http.StatusRequestTimeout)
		return
	}
	s.receiveUpload(w, r, upload, boundary, progress, deadline, false)
}

// handleSharedUpload receives an upload through an upload link, from the
// page that handleUploadLink serves. The link's token authorizes it; there is
// no session, so there is no CSRF token to check.
func (s *Server) handleSharedUpload(w http.ResponseWriter, r *http.Request) {
	secret := r.PathValue("token")
	if !s.sameOrigin(r) {
		http.Error(w, "invalid request origin", http.StatusForbidden)
		return
	}
	// Check the link before keeping any state for the request, so a made-up
	// token costs nothing. BeginSharedUpload, Prepare, and Publish check it
	// again, since it may be revoked at any time.
	if _, err := s.files.UploadLink(r.Context(), secret); errors.Is(err, share.ErrNotFound) {
		http.Error(w, uploadLinkGone, http.StatusNotFound)
		return
	} else if err != nil {
		writeUploadError(w, r, err, true)
		return
	}
	boundary, ok := multipartBoundary(w, r)
	if !ok {
		return
	}
	progress, ok := s.beginProgress(w, r, uploadOwner{link: token.Hash(secret)})
	if !ok {
		return
	}
	defer progress.fail()
	r, deadline, stop := stoppable(w, r, progress)
	defer stop()
	upload, err := s.files.BeginSharedUpload(r.Context(), secret, progress)
	if errors.Is(err, share.ErrNotFound) {
		http.Error(w, uploadLinkGone, http.StatusNotFound)
		return
	}
	if err != nil {
		writeUploadError(w, r, err, true)
		return
	}
	s.receiveUpload(w, r, upload, boundary, progress, deadline, true)
}

// uploadLinkGone answers a request through an upload link that was revoked,
// replaced, or expired, or whose folder changed.
const uploadLinkGone = "this upload link no longer works; ask for a new one"

// multipartBoundary returns the boundary of a multipart upload, or answers the
// request and reports false.
func multipartBoundary(w http.ResponseWriter, r *http.Request) (string, bool) {
	// Parse multipart manually so we can stream.
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		http.Error(w, "expected multipart form", http.StatusBadRequest)
		return "", false
	}
	if params["boundary"] == "" {
		http.Error(w, "missing multipart boundary", http.StatusBadRequest)
		return "", false
	}
	return params["boundary"], true
}

// beginProgress starts tracking the upload that r asked to follow, or answers
// the request and reports false.
func (s *Server) beginProgress(w http.ResponseWriter, r *http.Request, owner uploadOwner) (*uploadProgress, bool) {
	progress, err := s.uploads.begin(owner, r.Header.Get("X-Upload-ID"))
	switch {
	case errors.Is(err, errInvalidUploadID):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, errTooManyUploads):
		http.Error(w, err.Error(), http.StatusTooManyRequests)
	case err != nil:
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		return progress, true
	}
	return nil, false
}

// stoppable lets the uploader stop the upload that progress follows; see
// cancelUpload. Stopping cancels the returned request's context, which ends a
// wait for the upload slot or the account lock, and ends a read of the body
// that waits for data. The caller cancels the context when done.
func stoppable(w http.ResponseWriter, r *http.Request, progress *uploadProgress) (*http.Request, *bodyDeadline, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	deadline := newBodyDeadline(w)
	progress.onStop(func() {
		cancel()
		deadline.stop()
	})
	return r.WithContext(ctx), deadline, cancel
}

// receiveUpload streams the files of r into upload and publishes them. An
// upload through a link reveals nothing about its owner's storage: it sends
// no quota headers, and errors do not say how much space is left.
func (s *Server) receiveUpload(w http.ResponseWriter, r *http.Request, upload *files.Upload, boundary string, progress *uploadProgress, deadline *bodyDeadline, shared bool) {
	defer func() {
		if err := upload.Close(); err != nil {
			slog.Warn("could not discard staged upload files", "err", err)
		}
	}()
	usage, err := upload.Prepare(r.Context())
	if usage.Known() && !shared {
		setQuotaHeaders(w, usage)
	}
	var quotaErr *files.QuotaError
	if errors.As(err, &quotaErr) {
		http.Error(w, "disk quota exhausted", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		writeUploadError(w, r, err, shared)
		return
	}
	// A stop during preparation must not let the upload start reading.
	if err := r.Context().Err(); err != nil {
		writeUploadError(w, r, err, shared)
		return
	}

	// The deadline stays in place on failure: the server then reads the rest of
	// a small body before responding, and must not wait for a stalled client.
	body := newIdleTimeoutReader(r.Body, deadline, s.uploadTimeout)
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
				writeQuotaError(w, quotaErr, shared)
			case errors.Is(err, storage.ErrInvalidPath), errors.Is(err, storage.ErrConflict):
				writeStorageError(w, err)
			case errors.As(err, &fileErr):
				serverError(w, r, "upload failed", err)
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
	if receipt.Usage.Known() && !shared {
		setQuotaHeaders(w, receipt.Usage)
	}
	if err != nil {
		writeUploadError(w, r, err, shared)
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
func writeUploadError(w http.ResponseWriter, r *http.Request, err error, shared bool) {
	var quotaErr *files.QuotaError
	switch {
	case errors.Is(err, share.ErrNotFound):
		http.Error(w, uploadLinkGone, http.StatusNotFound)
	case errors.Is(err, account.ErrInvalidCredentials) && shared:
		// The owner's password changed while the files streamed.
		http.Error(w, "the upload could not be saved; try again", http.StatusConflict)
	case errors.Is(err, account.ErrInvalidCredentials):
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	case errors.Is(err, files.ErrUsageUnavailable):
		serverError(w, r, "failed to check storage usage", err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "upload canceled", http.StatusRequestTimeout)
	case errors.Is(err, files.ErrQuotaUnavailable):
		serverError(w, r, "failed to load disk quota", err)
	case errors.As(err, &quotaErr):
		writeQuotaError(w, quotaErr, shared)
	default:
		writeStorageError(w, err)
	}
}

// writeQuotaError answers an upload that does not fit the quota. Only the
// account's owner learns how much space is left.
func writeQuotaError(w http.ResponseWriter, err *files.QuotaError, shared bool) {
	message := fmt.Sprintf("disk quota exceeded (%s available)", size.Format(err.Available))
	if shared {
		message = "there is not enough storage space for these files"
	}
	http.Error(w, message, http.StatusRequestEntityTooLarge)
}

func uploadReadErrorStatus(err error) int {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return http.StatusRequestEntityTooLarge
	}
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.Canceled) {
		return http.StatusRequestTimeout
	}
	return http.StatusBadRequest
}
