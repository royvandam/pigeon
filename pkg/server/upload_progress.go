package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"
)

const uploadProgressRetention = 5 * time.Minute

var uploadIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,64}$`)

type uploadProgressKey struct {
	userID int64
	id     string
}

type uploadProgressSnapshot struct {
	State string `json:"state"`
	Bytes int64  `json:"bytes_received"`
}

type uploadProgress struct {
	mu       sync.Mutex
	snapshot uploadProgressSnapshot
	finished time.Time
}

// uploadTracker keeps the progress of uploads that asked for it. Progress
// belongs to the authenticated user and lives only in this server. Terminal
// records expire so callers can fetch a final snapshot without keeping an
// ever-growing upload history in memory.
type uploadTracker struct {
	progress sync.Map
}

func newUploadTracker() *uploadTracker {
	return &uploadTracker{}
}

// begin starts tracking upload id of a user, or returns nil when the upload
// has no ID.
func (t *uploadTracker) begin(userID int64, id string) (*uploadProgress, error) {
	if id == "" {
		return nil, nil // Existing API clients need not request progress tracking.
	}
	if !uploadIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid upload ID")
	}
	now := time.Now()
	t.progress.Range(func(key, value any) bool {
		p := value.(*uploadProgress)
		p.mu.Lock()
		expired := !p.finished.IsZero() && now.Sub(p.finished) >= uploadProgressRetention
		p.mu.Unlock()
		if expired {
			t.progress.CompareAndDelete(key, p)
		}
		return true
	})
	p := &uploadProgress{snapshot: uploadProgressSnapshot{State: "queued"}}
	if _, loaded := t.progress.LoadOrStore(uploadProgressKey{userID, id}, p); loaded {
		return nil, fmt.Errorf("upload ID already in use")
	}
	return p, nil
}

func (p *uploadProgress) setState(state string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot.State = state
	if state == "complete" {
		p.finished = time.Now()
	}
}

func (p *uploadProgress) fail() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snapshot.State != "complete" {
		p.snapshot.State = "failed"
		p.finished = time.Now()
	}
}

// Preparing records that the upload has started checking the account's storage.
func (p *uploadProgress) Preparing() {
	p.setState("preparing")
}

// Written counts file bytes once storage has accepted them, so multipart
// framing and bytes still buffered in the browser or a proxy do not count.
func (p *uploadProgress) Written(n int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.snapshot.Bytes += n
	p.mu.Unlock()
}

// snapshot returns the progress of a user's upload, if it is still tracked.
func (t *uploadTracker) snapshot(userID int64, id string) (uploadProgressSnapshot, bool) {
	key := uploadProgressKey{userID, id}
	value, ok := t.progress.Load(key)
	if !ok {
		return uploadProgressSnapshot{}, false
	}
	p := value.(*uploadProgress)
	p.mu.Lock()
	expired := !p.finished.IsZero() && time.Since(p.finished) >= uploadProgressRetention
	snapshot := p.snapshot
	p.mu.Unlock()
	if expired {
		t.progress.CompareAndDelete(key, p)
		return uploadProgressSnapshot{}, false
	}
	return snapshot, true
}

func (s *Server) handleUploadProgress(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	a, ok := s.requireAuth(w, r, authOptions{})
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if !uploadIDPattern.MatchString(id) {
		http.Error(w, "invalid upload ID", http.StatusBadRequest)
		return
	}
	snapshot, ok := s.uploads.snapshot(a.User.ID, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshot)
}
