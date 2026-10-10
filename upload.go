package main

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

	access, err := cfg.accessAccount(r.Context(), a.User)
	if err != nil {
		if errors.Is(err, errInvalidCredentials) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, "upload canceled", http.StatusRequestTimeout)
		} else {
			http.Error(w, "failed to load disk quota", http.StatusInternalServerError)
		}
		return
	}
	defer access.close()
	progress.setState("preparing")
	quota := access.QuotaBytes
	files := access.Files
	dir, err := files.path()
	if err != nil {
		writeStorageError(w, err)
		return
	}
	root, err := files.open()
	if err != nil {
		writeStorageError(w, err)
		return
	}
	root.Close()
	used, err := files.usage(r.Context())
	if err != nil {
		http.Error(w, "failed to check storage usage", http.StatusInternalServerError)
		return
	}
	remaining := remainingQuota(quota, used)
	setQuotaHeaders(w, quota, used)
	if remaining == 0 {
		http.Error(w, "disk quota exhausted", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, remaining+multipartOverhead)
	defer r.Body.Close()
	mr := multipart.NewReader(r.Body, boundary)
	batch, err := newUploadBatch(dir, r.URL.Query().Get("dir"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer batch.close()
	batch.progress = progress
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
		if err := batch.checkPath(name); err != nil {
			writeStorageError(w, err)
			return
		}
		if err := batch.add(name, part, remaining-batch.bytes); err != nil {
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
	if r.Context().Err() != nil {
		http.Error(w, "upload canceled", http.StatusRequestTimeout)
		return
	}
	progress.setState("finalizing")
	savedName, group, err := batch.publish()
	if err != nil {
		writeStorageError(w, err)
		return
	}
	progress.setState("complete")

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	setQuotaHeaders(w, quota, used+batch.bytes)
	fmt.Fprintf(w, "ok\nsaved_as=%s\nbytes=%d\n", savedName, batch.bytes)
	if group {
		fmt.Fprintf(w, "files=%d\n", batch.files)
	}
}

func uploadReadErrorStatus(err error) int {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}
