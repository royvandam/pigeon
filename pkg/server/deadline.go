package server

import (
	"io"
	"net/http"
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

// idleTimeoutReader keeps extending the read deadline while data arrives, so a
// long upload may continue but a stalled one fails after the timeout.
type idleTimeoutReader struct {
	io.ReadCloser
	controller *http.ResponseController
	timeout    time.Duration
	extended   time.Time
}

func newIdleTimeoutReader(w http.ResponseWriter, body io.ReadCloser, timeout time.Duration) *idleTimeoutReader {
	return &idleTimeoutReader{ReadCloser: body, controller: http.NewResponseController(w), timeout: timeout}
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	// Extending at most every eighth of the timeout keeps the cost low for many
	// small reads; a stall is still detected after 7/8 to all of the timeout.
	if now := time.Now(); now.Sub(r.extended) >= r.timeout/8 {
		_ = r.controller.SetReadDeadline(now.Add(r.timeout))
		r.extended = now
	}
	return r.ReadCloser.Read(p)
}

// clear removes the deadline once the body has been read; see limitBodyRead.
func (r *idleTimeoutReader) clear() {
	_ = r.controller.SetReadDeadline(time.Time{})
}
