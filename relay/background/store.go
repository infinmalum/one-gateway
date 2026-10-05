// Package background stores in-flight and finished background Responses so
// clients can retrieve and cancel them by ID. Entries live in memory: the
// gateway keeps the upstream result only until the TTL expires, which covers
// the polling window a client needs.
package background

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Response statuses shared with the Responses wire format.
const (
	StatusQueued     = "queued"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusCancelled  = "cancelled"
)

// maxEntries bounds the store so a flood of background creates cannot exhaust
// memory. The oldest entry is evicted when the cap is reached.
const maxEntries = 512

// Runner executes one gateway request. It must write the upstream response or
// error body to dst, including the status code, before returning.
type Runner func(ctx context.Context, dst http.ResponseWriter)

// Entry is one background response. ID, model, owner, and creation time are
// immutable; status changes exactly once to a terminal value.
type Entry struct {
	id        string
	model     string
	ownerID   int
	createdAt int64

	mu           sync.Mutex
	status       string
	body         []byte
	errMessage   string
	errCode      string
	terminatedAt time.Time
	cancel       context.CancelFunc
}

// ID returns the gateway-assigned response identifier.
func (e *Entry) ID() string { return e.id }

// Owner returns the user ID allowed to retrieve or cancel the entry.
func (e *Entry) Owner() int { return e.ownerID }

// Status reports the current lifecycle status.
func (e *Entry) Status() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

// Cancel requests cancellation. Cancelling a terminal entry is a no-op, so
// repeated calls are idempotent.
func (e *Entry) Cancel() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if terminal(e.status) {
		return
	}
	e.status = StatusCancelled
	e.terminatedAt = time.Now()
	if e.cancel != nil {
		e.cancel()
	}
}

// Snapshot renders the current response object and the HTTP status with which
// retrieval should serve it.
func (e *Entry) Snapshot() (int, []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch e.status {
	case StatusCompleted:
		return http.StatusOK, e.body
	case StatusFailed:
		return http.StatusOK, e.render(map[string]any{"code": e.errCode, "message": e.errMessage, "param": nil})
	default:
		return http.StatusOK, e.render(nil)
	}
}

// render builds the response object for entries without an upstream body.
func (e *Entry) render(errorField any) []byte {
	body, err := json.Marshal(map[string]any{
		"id": e.id, "object": "response", "created_at": e.createdAt,
		"status": e.status, "model": e.model, "error": errorField,
	})
	if err != nil {
		return []byte(`{"object":"response","status":"failed"}`)
	}
	return body
}

// finish moves the entry to a terminal status exactly once.
func (e *Entry) finish(status string) {
	if terminal(e.status) {
		return
	}
	e.status = status
	e.terminatedAt = time.Now()
}

func terminal(status string) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// Store holds background entries and removes terminal ones after the TTL.
type Store struct {
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]*Entry
	order   []string
}

// NewStore creates a store that keeps terminal entries for ttl. A janitor
// goroutine sweeps expired entries every minute until Close is called.
func NewStore(ttl time.Duration) *Store {
	return &Store{ttl: ttl, now: time.Now, entries: make(map[string]*Entry)}
}

var (
	defaultOnce sync.Once
	defaultSt   *Store
)

// Default returns the process-wide store used by the Responses routes. A
// janitor goroutine sweeps expired terminal entries every ten minutes.
func Default() *Store {
	defaultOnce.Do(func() {
		defaultSt = NewStore(time.Hour)
		go func() {
			ticker := time.NewTicker(10 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				defaultSt.Sweep()
			}
		}()
	})
	return defaultSt
}

// Lookup returns the entry with the given ID, or nil when it is unknown or
// its terminal result has expired.
func (s *Store) Lookup(id string) *Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil {
		return nil
	}
	entry.mu.Lock()
	expired := terminal(entry.status) && s.now().After(entry.terminatedAt.Add(s.ttl))
	entry.mu.Unlock()
	if expired {
		return nil
	}
	return entry
}

// Start registers a queued entry and executes run in a new goroutine with a
// context detached from the HTTP request, so the create call can return
// immediately. Ending the caller's request does not stop the task; only
// Entry.Cancel does.
func (s *Store) Start(ctx context.Context, ownerID int, modelName string, run Runner) *Entry {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	entry := &Entry{
		id:        newResponseID(),
		model:     modelName,
		ownerID:   ownerID,
		createdAt: s.now().Unix(),
		status:    StatusQueued,
		cancel:    cancel,
	}
	s.add(entry)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				entry.mu.Lock()
				if !terminal(entry.status) {
					entry.finish(StatusFailed)
					entry.errCode = "server_error"
					entry.errMessage = "background response processing failed"
				}
				entry.mu.Unlock()
			}
			cancel()
		}()
		entry.mu.Lock()
		if entry.status == StatusQueued {
			entry.status = StatusInProgress
		}
		entry.mu.Unlock()
		writer := &bufferWriter{header: make(http.Header)}
		run(runCtx, writer)
		entry.finalize(runCtx, writer)
	}()
	return entry
}

// finalize records the runner outcome. A cancellation always wins, even when
// the upstream managed to finish before the cancelled request was observed.
func (e *Entry) finalize(ctx context.Context, writer *bufferWriter) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if terminal(e.status) {
		return
	}
	if ctx.Err() != nil {
		e.finish(StatusCancelled)
		return
	}
	if status := writer.statusCode(); status < 200 || status > 299 {
		e.finish(StatusFailed)
		e.errCode = "upstream_error"
		e.errMessage = upstreamErrorMessage(writer.body(), "upstream request failed")
		return
	}
	completed, err := withResponseIdentity(writer.body(), e.id)
	if err != nil {
		e.finish(StatusFailed)
		e.errCode = "server_error"
		e.errMessage = "upstream returned a non-JSON response"
		return
	}
	e.finish(StatusCompleted)
	e.body = completed
}

func (e *Entry) renderLocked(errorField any) []byte {
	body, err := json.Marshal(map[string]any{
		"id": e.id, "object": "response", "created_at": e.createdAt,
		"status": e.status, "model": e.model, "error": errorField,
	})
	if err != nil {
		return []byte(`{"object":"response","status":"failed"}`)
	}
	return body
}

func (s *Store) add(entry *Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) >= maxEntries {
		oldest := s.order[0]
		delete(s.entries, oldest)
		s.order = s.order[1:]
	}
	s.entries[entry.id] = entry
	s.order = append(s.order, entry.id)
}

// Sweep removes terminal entries whose TTL has expired. Queued and in-flight
// entries are never removed.
func (s *Store) Sweep() {
	cutoff := s.now().Add(-s.ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.order[:0]
	for _, id := range s.order {
		entry := s.entries[id]
		if entry == nil {
			continue
		}
		entry.mu.Lock()
		remove := !entry.terminatedAt.IsZero() && entry.terminatedAt.Before(cutoff)
		entry.mu.Unlock()
		if remove {
			delete(s.entries, id)
			continue
		}
		kept = append(kept, id)
	}
	s.order = kept
}

func newResponseID() string {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "resp_" + hex.EncodeToString([]byte(time.Now().Format("060102150405.000000000")))
	}
	return "resp_" + hex.EncodeToString(raw[:])
}

// withResponseIdentity rewrites the upstream response object so retrieval
// returns the gateway-assigned ID while preserving every other field.
func withResponseIdentity(body []byte, id string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return nil, err
	}
	encoded, _ := json.Marshal(id)
	payload["id"] = encoded
	if _, ok := payload["object"]; !ok {
		payload["object"], _ = json.Marshal("response")
	}
	if _, ok := payload["status"]; !ok {
		payload["status"], _ = json.Marshal(StatusCompleted)
	}
	return json.Marshal(payload)
}

// bufferWriter records what the runner wrote so the store can decide the
// final status from the response status code and body.
type bufferWriter struct {
	header http.Header
	buf    bytes.Buffer
	status int
}

func (w *bufferWriter) Header() http.Header { return w.header }

func (w *bufferWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *bufferWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *bufferWriter) body() []byte { return w.buf.Bytes() }

// upstreamErrorMessage extracts the error message from a forwarded upstream
// error body without failing when the body is not the expected envelope.
func upstreamErrorMessage(body []byte, fallback string) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error.Message != "" {
		return payload.Error.Message
	}
	return fallback
}
