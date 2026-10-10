package main

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
)

const multipartOverhead = int64(10 << 20)

func (cfg config) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Authenticate and check the session CSRF header before touching the body.
	a, ok := cfg.requireAuth(w, r, false, false)
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

	unlock, err := cfg.store.lockUploads(r.Context(), a.User.ID)
	if err != nil {
		http.Error(w, "upload canceled", http.StatusRequestTimeout)
		return
	}
	defer unlock()
	progress.setState("preparing")
	quota, err := cfg.store.userQuota(r.Context(), a.User)
	if err != nil {
		if errors.Is(err, errInvalidCredentials) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		} else {
			http.Error(w, "failed to load disk quota", http.StatusInternalServerError)
		}
		return
	}
	dir := filepath.Join(cfg.dir, a.User.Slug)
	used, err := diskUsage(r.Context(), dir)
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
	if err := os.MkdirAll(dir, 0o750); err != nil {
		http.Error(w, "failed to create user directory", http.StatusInternalServerError)
		return
	}

	batch, err := newUploadBatch(dir)
	if err != nil {
		http.Error(w, "failed to prepare upload", http.StatusInternalServerError)
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
			http.Error(w, err.Error(), http.StatusBadRequest)
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
		http.Error(w, "failed to save upload", http.StatusInternalServerError)
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
