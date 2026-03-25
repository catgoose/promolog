// Package promolog provides per-request error trace capture with
// promote-on-error semantics. Each request buffers its slog records locally;
// only when an error occurs is the buffer promoted to a SQLite-backed Store
// for later retrieval.
package promolog

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrDuplicateTrace is returned when a trace with the same request ID already exists.
var ErrDuplicateTrace = errors.New("promolog: duplicate request ID")

// RequestIDKey is the context key used to associate a request ID with a context.
// CorrelationMiddleware sets this automatically. For custom setups:
//
//	ctx = context.WithValue(ctx, promolog.RequestIDKey, "req-123")
type requestIDKeyType struct{}

var RequestIDKey = requestIDKeyType{}

// Entry is a single captured log record.
type Entry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"msg"`
	Attrs   string    `json:"attrs,omitempty"`
}

// Buffer is a per-request log buffer stored in the request context.
// It is safe for concurrent use.
type Buffer struct {
	mu      sync.Mutex
	Entries []Entry
}

// Append adds an entry to the buffer. It is safe for concurrent use.
func (b *Buffer) Append(e Entry) {
	b.mu.Lock()
	b.Entries = append(b.Entries, e)
	b.mu.Unlock()
}

// Snapshot returns a copy of the current entries. It is safe for concurrent use.
func (b *Buffer) Snapshot() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := make([]Entry, len(b.Entries))
	copy(cp, b.Entries)
	return cp
}

type bufferKey struct{}

// NewBufferContext returns a new context with an empty Buffer attached.
func NewBufferContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, bufferKey{}, &Buffer{})
}

// GetBuffer retrieves the per-request Buffer from the context, or nil.
func GetBuffer(ctx context.Context) *Buffer {
	buf, _ := ctx.Value(bufferKey{}).(*Buffer)
	return buf
}

// ErrorTrace contains all the information captured when a request errors.
type ErrorTrace struct {
	RequestID  string
	ErrorChain string
	StatusCode int
	Route      string
	Method     string
	UserAgent  string
	RemoteIP   string
	UserID     string
	Entries    []Entry
	CreatedAt  time.Time
}

// TraceSummary is a lightweight row for list views (no log entries).
type TraceSummary struct {
	RequestID  string
	ErrorChain string
	StatusCode int
	Route      string
	Method     string
	RemoteIP   string
	UserID     string
	CreatedAt  time.Time
}

// TraceFilter holds all filter parameters for ListTraces.
type TraceFilter struct {
	Q       string
	Status  string
	Method  string
	Sort    string
	Dir     string
	Page    int
	PerPage int
}

// FilterOptions holds distinct values available for filter dropdowns.
type FilterOptions struct {
	StatusCodes []int
	Methods     []string
}

// Storer defines the interface for trace persistence. Useful for mocking in tests.
type Storer interface {
	InitSchema() error
	SetOnPromote(fn func(TraceSummary))
	Promote(ctx context.Context, trace ErrorTrace) error
	PromoteAt(ctx context.Context, trace ErrorTrace, createdAt time.Time) error
	Get(ctx context.Context, requestID string) (*ErrorTrace, error)
	ListTraces(ctx context.Context, f TraceFilter) ([]TraceSummary, int, error)
	AvailableFilters(ctx context.Context, f TraceFilter) (FilterOptions, error)
	DeleteTrace(ctx context.Context, requestID string) error
	StartCleanup(ctx context.Context, ttl time.Duration, interval time.Duration)
}

// compile-time check
var _ Storer = (*Store)(nil)
