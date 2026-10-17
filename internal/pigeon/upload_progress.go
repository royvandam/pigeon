package pigeon

import (
	"encoding/json"
	"fmt"
	"io"
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

// Progress belongs to the authenticated user and lives only in this server.
// Terminal records expire so callers can fetch a final snapshot without keeping
// an ever-growing upload history in memory.
func (s *store) beginUploadProgress(userID int64, id string) (*uploadProgress, error) {
	if id == "" {
		return nil, nil // Existing API clients need not request progress tracking.
	}
	if !uploadIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid upload ID")
	}
	now := time.Now()
	s.progress.Range(func(key, value any) bool {
		p := value.(*uploadProgress)
		p.mu.Lock()
		expired := !p.finished.IsZero() && now.Sub(p.finished) >= uploadProgressRetention
		p.mu.Unlock()
		if expired {
			s.progress.CompareAndDelete(key, p)
		}
		return true
	})
	p := &uploadProgress{snapshot: uploadProgressSnapshot{State: "queued"}}
	if _, loaded := s.progress.LoadOrStore(uploadProgressKey{userID, id}, p); loaded {
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

func (p *uploadProgress) addBytes(n int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.snapshot.Bytes += n
	p.mu.Unlock()
}

func (s *store) uploadProgress(userID int64, id string) (uploadProgressSnapshot, bool) {
	key := uploadProgressKey{userID, id}
	value, ok := s.progress.Load(key)
	if !ok {
		return uploadProgressSnapshot{}, false
	}
	p := value.(*uploadProgress)
	p.mu.Lock()
	expired := !p.finished.IsZero() && time.Since(p.finished) >= uploadProgressRetention
	snapshot := p.snapshot
	p.mu.Unlock()
	if expired {
		s.progress.CompareAndDelete(key, p)
		return uploadProgressSnapshot{}, false
	}
	return snapshot, true
}

func (cfg config) handleUploadProgress(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	a, ok := cfg.requireAuth(w, r, authOptions{})
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if !uploadIDPattern.MatchString(id) {
		http.Error(w, "invalid upload ID", http.StatusBadRequest)
		return
	}
	snapshot, ok := cfg.store.uploadProgress(a.User.ID, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshot)
}

// Count file bytes only after the output writer accepts them. Multipart framing
// and bytes still buffered in the browser or a proxy do not advance progress.
type uploadProgressWriter struct {
	io.Writer
	progress *uploadProgress
}

func (w uploadProgressWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	w.progress.addBytes(int64(n))
	return n, err
}
