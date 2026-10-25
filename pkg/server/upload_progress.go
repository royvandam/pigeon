package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sync"
	"time"
)

const uploadProgressRetention = 5 * time.Minute

// maxTrackedUploads is how many uploads one account or upload link may have
// tracked at a time. A finished record makes room for a new one, so only
// uploads still in progress count against it, and nobody can grow the tracker
// without bound.
const maxTrackedUploads = 16

var uploadIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,64}$`)

var (
	errInvalidUploadID = errors.New("invalid upload ID")
	errUploadIDInUse   = errors.New("upload ID already in use")
	errTooManyUploads  = errors.New("too many uploads in progress; wait for one to finish")
)

// uploadOwner is whose uploads a progress record belongs to: an account's, or
// those through one upload link, identified by its token's hash.
type uploadOwner struct {
	userID int64
	link   string
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
// belongs to the authenticated user, or to the upload link it came through,
// and lives only in this server. Finished records expire so callers can fetch
// a final snapshot without keeping an ever-growing upload history in memory.
type uploadTracker struct {
	mu        sync.Mutex
	uploads   map[uploadOwner]map[string]*uploadProgress
	lastSweep time.Time
}

func newUploadTracker() *uploadTracker {
	return &uploadTracker{uploads: make(map[uploadOwner]map[string]*uploadProgress)}
}

// begin starts tracking upload id of owner, or returns nil when the upload
// has no ID. It fails when owner already tracks maxTrackedUploads uploads in
// progress.
func (t *uploadTracker) begin(owner uploadOwner, id string) (*uploadProgress, error) {
	if id == "" {
		return nil, nil // Existing API clients need not request progress tracking.
	}
	if !uploadIDPattern.MatchString(id) {
		return nil, errInvalidUploadID
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	// Owners who stopped uploading leave expired records behind. Sweeping them
	// once per retention period keeps each upload's work proportional to its
	// own owner's records.
	if now.Sub(t.lastSweep) >= uploadProgressRetention {
		for other := range t.uploads {
			t.prune(other, now)
		}
		t.lastSweep = now
	} else {
		t.prune(owner, now)
	}
	records := t.uploads[owner]
	if _, ok := records[id]; ok {
		return nil, errUploadIDInUse
	}
	if len(records) >= maxTrackedUploads && !evictOldestFinished(records) {
		return nil, errTooManyUploads
	}
	if records == nil {
		records = make(map[string]*uploadProgress)
		t.uploads[owner] = records
	}
	p := &uploadProgress{snapshot: uploadProgressSnapshot{State: "queued"}}
	records[id] = p
	return p, nil
}

// prune removes owner's expired records, and owner once it has none left.
// The tracker's lock must be held.
func (t *uploadTracker) prune(owner uploadOwner, now time.Time) {
	records := t.uploads[owner]
	for id, p := range records {
		if p.expired(now) {
			delete(records, id)
		}
	}
	if len(records) == 0 {
		delete(t.uploads, owner)
	}
}

// evictOldestFinished removes the finished record that finished first, and
// reports false if every record is still in progress.
func evictOldestFinished(records map[string]*uploadProgress) bool {
	oldest, oldestTime := "", time.Time{}
	for id, p := range records {
		p.mu.Lock()
		finished := p.finished
		p.mu.Unlock()
		if !finished.IsZero() && (oldest == "" || finished.Before(oldestTime)) {
			oldest, oldestTime = id, finished
		}
	}
	if oldest == "" {
		return false
	}
	delete(records, oldest)
	return true
}

// expired reports whether p finished at least uploadProgressRetention ago.
func (p *uploadProgress) expired(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.finished.IsZero() && now.Sub(p.finished) >= uploadProgressRetention
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

// snapshot returns the progress of owner's upload, if it is still tracked.
func (t *uploadTracker) snapshot(owner uploadOwner, id string) (uploadProgressSnapshot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.uploads[owner][id]
	if !ok {
		return uploadProgressSnapshot{}, false
	}
	if now := time.Now(); p.expired(now) {
		t.prune(owner, now)
		return uploadProgressSnapshot{}, false
	}
	p.mu.Lock()
	snapshot := p.snapshot
	p.mu.Unlock()
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
	s.writeUploadProgress(w, r, uploadOwner{userID: a.User.ID})
}

func (s *Server) writeUploadProgress(w http.ResponseWriter, r *http.Request, owner uploadOwner) {
	id := r.URL.Query().Get("id")
	if !uploadIDPattern.MatchString(id) {
		http.Error(w, "invalid upload ID", http.StatusBadRequest)
		return
	}
	snapshot, ok := s.uploads.snapshot(owner, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshot)
}
