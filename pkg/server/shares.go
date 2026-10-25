package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"time"

	"github.com/royvandam/pigeon/internal/token"
	"github.com/royvandam/pigeon/pkg/share"
)

// shareStatus is the JSON answer of /shares.
type shareStatus struct {
	// URL is the path of a link, known only right after it is created.
	URL    string     `json:"url"`
	Active bool       `json:"active"`
	Kind   share.Kind `json:"kind"`
	// ExpiresAt is when an active link stops working, or null if it does not
	// expire.
	ExpiresAt *time.Time `json:"expires_at"`
}

func (s *Server) handleShares(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !allowMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	a, ok := s.requireAuth(w, r, authOptions{})
	if !ok {
		return
	}
	form, action := r.URL.Query(), ""
	if r.Method == http.MethodPost {
		form, action = r.PostForm, r.PostForm.Get("action")
		if action != "create" && action != "revoke" {
			http.Error(w, "unknown share action", http.StatusBadRequest)
			return
		}
	}
	name := form.Get("path")
	kind, err := share.ParseKind(form.Get("kind"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	lifetime, err := parseLifetime(form.Get("lifetime"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	access, err := s.files.Open(r.Context(), a.User)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	defer access.Close()
	answer := shareStatus{Kind: kind}
	var status share.Status
	switch action {
	case "":
		status, err = access.Shared(r.Context(), name, kind)
	case "create":
		var secret string
		secret, status.Expires, err = access.Share(r.Context(), name, kind, lifetime)
		answer.URL, status.Active = linkPath(kind, secret), err == nil
	case "revoke":
		err = access.Unshare(r.Context(), name, kind)
	}
	if err != nil {
		writeStorageError(w, err)
		return
	}
	answer.Active = status.Active
	if !status.Expires.IsZero() {
		expires := status.Expires.UTC()
		answer.ExpiresAt = &expires
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(answer)
}

// parseLifetime reads how long a new link lasts, such as "24h"; "" means it
// does not expire.
func parseLifetime(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	lifetime, err := time.ParseDuration(value)
	if err != nil || lifetime <= 0 {
		return 0, fmt.Errorf("invalid link lifetime %q: use a duration such as \"24h\", or none", value)
	}
	return lifetime, nil
}

// linkPath returns the path that a link of kind with token secret is served
// at.
func linkPath(kind share.Kind, secret string) string {
	if kind == share.Upload {
		return "/u/" + secret
	}
	return "/s/" + secret
}

func (s *Server) handleSharedDownload(w http.ResponseWriter, r *http.Request) {
	setPublicLinkHeaders(w)
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	download, err := s.files.OpenShared(r.Context(), r.PathValue("token"))
	if errors.Is(err, share.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusRequestTimeout
			slog.Debug("shared download canceled", "err", err)
		} else {
			slog.Error("shared download unavailable", "err", err)
		}
		http.Error(w, "download unavailable", status)
		return
	}
	defer download.Close()
	serveDownload(w, r, download)
}

// setPublicLinkHeaders keeps pages and files behind public links out of
// caches, referrers, and search engines.
func setPublicLinkHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
}

// uploadLinkPage is the page behind an upload link.
type uploadLinkPage struct {
	// Folder is the name of the link's folder, or "" when the link no longer
	// works.
	Folder      string
	Expires     string
	UploadURL   string
	ProgressURL string
	// AllocationUnit lets the page round file sizes as the server charges
	// them.
	AllocationUnit int64
}

// handleUploadLink serves the page behind an upload link, and receives its
// uploads. The page shows the folder's name only, never what it holds.
func (s *Server) handleUploadLink(w http.ResponseWriter, r *http.Request) {
	setPublicLinkHeaders(w)
	if !allowMethods(w, r, http.MethodGet, http.MethodHead, http.MethodPost) {
		return
	}
	if r.Method == http.MethodPost {
		s.handleSharedUpload(w, r)
		return
	}
	secret := r.PathValue("token")
	link, err := s.files.UploadLink(r.Context(), secret)
	if errors.Is(err, share.ErrNotFound) {
		renderTemplate(w, uploadLinkTmpl, uploadLinkPage{}, http.StatusNotFound)
		return
	}
	if err != nil {
		serverError(w, r, "upload link unavailable", err)
		return
	}
	page := uploadLinkPage{
		Folder:         path.Base(link.Path),
		UploadURL:      "/u/" + secret,
		ProgressURL:    "/u/" + secret + "/progress",
		AllocationUnit: s.files.AllocationUnit(),
	}
	if !link.Expires.IsZero() {
		page.Expires = link.Expires.UTC().Format("2 January 2006, 15:04 UTC")
	}
	renderTemplate(w, uploadLinkTmpl, page, http.StatusOK)
}

// handleUploadLinkProgress reports the progress of an upload through an
// upload link to the page that sent it, or stops it when posted
// action=cancel.
func (s *Server) handleUploadLinkProgress(w http.ResponseWriter, r *http.Request) {
	setPublicLinkHeaders(w)
	if !allowMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	if r.Method == http.MethodPost {
		if !s.sameOrigin(r) {
			http.Error(w, "invalid request origin", http.StatusForbidden)
			return
		}
		if !s.parseForm(w, r) {
			return
		}
	}
	secret := r.PathValue("token")
	if _, err := s.files.UploadLink(r.Context(), secret); errors.Is(err, share.ErrNotFound) {
		http.Error(w, uploadLinkGone, http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, r, "upload link unavailable", err)
		return
	}
	if r.Method == http.MethodPost {
		s.stopUpload(w, r, uploadOwner{link: token.Hash(secret)})
		return
	}
	s.writeUploadProgress(w, r, uploadOwner{link: token.Hash(secret)})
}
