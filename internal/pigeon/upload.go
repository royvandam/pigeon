package pigeon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
)

const multipartOverhead = int64(10 << 20)

func (cfg config) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}

	// Authenticate and check the session CSRF header before touching the body.
	a, ok := cfg.requireAuth(w, r, authOptions{})
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
	progress, err := cfg.store.beginUploadProgress(a.User.ID, r.Header.Get("X-Upload-ID"))
	if err != nil {
		status := http.StatusConflict
		if !uploadIDPattern.MatchString(r.Header.Get("X-Upload-ID")) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	defer progress.fail()

	// The account lock is held only to check the quota and to publish, so the
	// account's pages, share links, and file actions do not wait for the body.
	// The slot keeps this account's uploads streaming one at a time.
	releaseSlot, err := cfg.store.lockUploadSlot(r.Context(), a.User.ID)
	if err != nil {
		http.Error(w, "upload canceled", http.StatusRequestTimeout)
		return
	}
	defer releaseSlot()
	access, ok := cfg.accessUploadAccount(w, r, a.User)
	if !ok {
		return
	}
	defer access.close()
	progress.setState("preparing")
	quota := access.QuotaBytes
	files := access.Files
	tree, err := files.open(r.Context())
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer tree.Close()
	used, err := access.used(r.Context())
	if err != nil {
		http.Error(w, "failed to check storage usage", http.StatusInternalServerError)
		return
	}
	remaining := remainingQuota(quota, used)
	setQuotaHeaders(w, quota, used)
	if remaining < files.unit {
		http.Error(w, "disk quota exhausted", http.StatusRequestEntityTooLarge)
		return
	}
	batch, err := newUploadBatch(r.Context(), tree, r.URL.Query().Get("dir"), remaining, files.unit)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer batch.close()
	batch.progress = progress
	access.close()

	// The deadline stays in place on failure: the server then reads the rest of
	// a small body before responding, and must not wait for a stalled client.
	body := newIdleTimeoutReader(w, r.Body, cfg.uploadTimeout())
	r.Body = http.MaxBytesReader(w, body, remaining+multipartOverhead)
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
		name := disposition["filename"]
		if err := batch.checkPath(r.Context(), name); err != nil {
			writeStorageError(w, err)
			return
		}
		if err := batch.add(r.Context(), name, part); err != nil {
			if errors.Is(err, errDiskQuotaExceeded) {
				http.Error(w, fmt.Sprintf("disk quota exceeded (%s available)", humanBytes(remaining)), http.StatusRequestEntityTooLarge)
			} else {
				var fileErr *os.PathError
				status := uploadReadErrorStatus(err)
				if errors.As(err, &fileErr) {
					status = http.StatusInternalServerError
				}
				http.Error(w, "upload failed", status)
			}
			return
		}
		part.Close()
	}

	if batch.files == 0 {
		http.Error(w, "missing file", http.StatusBadRequest)
		return
	}
	body.clear()
	if r.Context().Err() != nil {
		http.Error(w, "upload canceled", http.StatusRequestTimeout)
		return
	}
	progress.setState("finalizing")
	// The account may have been deleted, its quota lowered, or its storage
	// filled by file actions while the body streamed.
	access, ok = cfg.accessUploadAccount(w, r, a.User)
	if !ok {
		return
	}
	defer access.close()
	quota = access.QuotaBytes
	used, err = access.used(r.Context()) // Staging folders are never counted.
	if err != nil {
		http.Error(w, "failed to check storage usage", http.StatusInternalServerError)
		return
	}
	charged, err := batch.currentCharge(r.Context())
	if err != nil {
		writeStorageError(w, err)
		return
	}
	setQuotaHeaders(w, quota, used)
	if remainingQuota(quota, used) < charged {
		http.Error(w, fmt.Sprintf("disk quota exceeded (%s available)", humanBytes(remainingQuota(quota, used))), http.StatusRequestEntityTooLarge)
		return
	}
	savedName, group, err := batch.publish(r.Context())
	if err != nil {
		access.forgetUsage() // Publication rolls back, but only as far as it can.
		writeStorageError(w, err)
		return
	}
	access.changed(charged)
	progress.setState("complete")

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	setQuotaHeaders(w, quota, used+charged)
	fmt.Fprintf(w, "ok\nsaved_as=%s\nbytes=%d\n", savedName, batch.bytes)
	if group {
		fmt.Fprintf(w, "files=%d\n", batch.files)
	}
}

// accessUploadAccount locks the account and loads its quota, answering the
// request itself when that fails.
func (cfg config) accessUploadAccount(w http.ResponseWriter, r *http.Request, u user) (*accountAccess, bool) {
	access, err := cfg.accessAccount(r.Context(), u)
	if err == nil {
		return access, true
	}
	if errors.Is(err, errInvalidCredentials) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		http.Error(w, "upload canceled", http.StatusRequestTimeout)
	} else {
		http.Error(w, "failed to load disk quota", http.StatusInternalServerError)
	}
	return nil, false
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
