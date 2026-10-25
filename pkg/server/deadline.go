package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// limitBodyRead sets a deadline for reading the rest of the request body. Call
// the returned function once the body has been read successfully: after the
// body ends, the server watches the connection for a disconnect, and an expired
// deadline would cancel the request while the handler is still working. Leave
// the deadline in place after a failure, because the server reads the rest of
// a small unread body before responding, and that read must not wait forever.
func limitBodyRead(w http.ResponseWriter, timeout time.Duration) func() {
	controller := http.NewResponseController(w)
	// Recorders in tests do not support deadlines; real connections always do.
	_ = controller.SetReadDeadline(time.Now().Add(timeout))
	return func() { _ = controller.SetReadDeadline(time.Time{}) }
}

// errReadStopped reports a body whose reading was stopped; see bodyDeadline.
var errReadStopped = fmt.Errorf("upload stopped: %w", context.Canceled)

// bodyDeadline sets a request's read deadline until stop ends the reading for
// good. stop expires the deadline, so a read waiting for data fails at once,
// and later extensions are refused rather than undoing it.
type bodyDeadline struct {
	mu         sync.Mutex
	stopped    bool
	controller *http.ResponseController
}

func newBodyDeadline(w http.ResponseWriter) *bodyDeadline {
	return &bodyDeadline{controller: http.NewResponseController(w)}
}

// set sets the read deadline to t, or reports false once stopped.
func (d *bodyDeadline) set(t time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return false
	}
	// Recorders in tests do not support deadlines; real connections do.
	_ = d.controller.SetReadDeadline(t)
	return true
}

// isStopped reports whether stop was called.
func (d *bodyDeadline) isStopped() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopped
}

// stop ends reading the body.
func (d *bodyDeadline) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	_ = d.controller.SetReadDeadline(time.Now())
}

// idleTimeoutReader keeps extending the read deadline while data arrives, so a
// long upload may continue but a stalled one fails after the timeout. It fails
// with errReadStopped once its deadline is stopped.
type idleTimeoutReader struct {
	io.ReadCloser
	deadline *bodyDeadline
	timeout  time.Duration
	extended time.Time
}

func newIdleTimeoutReader(body io.ReadCloser, deadline *bodyDeadline, timeout time.Duration) *idleTimeoutReader {
	return &idleTimeoutReader{ReadCloser: body, deadline: deadline, timeout: timeout}
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	// Extending at most every eighth of the timeout keeps the cost low for many
	// small reads; a stall is still detected after 7/8 to all of the timeout.
	if now := time.Now(); now.Sub(r.extended) >= r.timeout/8 {
		if !r.deadline.set(now.Add(r.timeout)) {
			return 0, errReadStopped
		}
		r.extended = now
	} else if r.deadline.isStopped() {
		return 0, errReadStopped
	}
	return r.ReadCloser.Read(p)
}

// clear removes the deadline once the body has been read, unless it was
// stopped; see limitBodyRead.
func (r *idleTimeoutReader) clear() {
	r.deadline.set(time.Time{})
}
