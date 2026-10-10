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
	"time"
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

	unlock, err := cfg.store.lockUploads(r.Context(), a.User.ID)
	if err != nil {
		http.Error(w, "upload canceled", http.StatusRequestTimeout)
		return
	}
	defer unlock()
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

	var (
		savedName string
		written   int64
	)

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
			part.Close()
			continue
		}

		// Prepare output file.
		orig := part.FileName()
		safe := sanitizeFilename(orig)
		if safe == "" {
			safe = "upload.bin"
		}

		name := fmt.Sprintf(
			"%s_%s_%s",
			time.Now().UTC().Format("20060102T150405Z"),
			shortRandHex(6),
			safe,
		)

		path := filepath.Join(dir, name)
		out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
		if err != nil {
			http.Error(w, "failed to create file", http.StatusInternalServerError)
			return
		}

		// Write at most the remaining quota, then probe for an extra byte without
		// writing it to disk. An oversized upload is removed in full.
		limited := &io.LimitedReader{
			R: part,
			N: remaining,
		}

		n, copyErr := io.Copy(out, limited)
		tooLarge := false
		if copyErr == nil && limited.N == 0 {
			var extra [1]byte
			_, probeErr := io.ReadFull(part, extra[:])
			if probeErr == nil {
				tooLarge = true
			} else if probeErr != io.EOF {
				copyErr = probeErr
			}
		}

		closeErr := out.Close()

		if copyErr != nil {
			os.Remove(path)
			http.Error(w, "upload failed", uploadReadErrorStatus(copyErr))
			return
		}

		if tooLarge {
			os.Remove(path)
			http.Error(
				w,
				fmt.Sprintf("disk quota exceeded (%s available)", humanBytes(remaining)),
				http.StatusRequestEntityTooLarge,
			)
			return
		}

		if closeErr != nil {
			os.Remove(path)
			http.Error(w, "failed to save file", http.StatusInternalServerError)
			return
		}

		part.Close()
		savedName = name
		written = n
		break
	}

	if savedName == "" {
		http.Error(w, "missing file", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	setQuotaHeaders(w, quota, used+written)
	fmt.Fprintf(w, "ok\nsaved_as=%s\nbytes=%d\n", savedName, written)
}

func uploadReadErrorStatus(err error) int {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}
