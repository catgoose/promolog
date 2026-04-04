// Package promolog provides per-request trace capture with
// promote-on-error semantics. Each request buffers its slog records locally;
// only when an error occurs is the buffer promoted to a Storer implementation
// for later retrieval. The core package has zero external dependencies.
// See github.com/catgoose/promolog/sqlite for a SQLite-backed Storer.
package promolog

import (
	"context"
	"errors"
	"fmt"
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
	Attrs   map[string]string `json:"attrs,omitempty"`
}

// Buffer is a per-request log buffer stored in the request context.
// It is safe for concurrent use.
//
// When a limit is set (via NewBuffer), the buffer keeps the first half and last
// half of entries. Middle entries are dropped and replaced with a synthetic
// entry indicating how many were elided. A limit of 0 means unlimited.
type Buffer struct {
	mu      sync.Mutex
	entries []Entry
	limit   int // 0 = unlimited
	head    []Entry
	tail    []Entry
	total   int // total entries appended (only tracked when limit > 0)
	elided  int // entries dropped from the middle
	tags    map[string]string
}

// Tag sets a key-value tag on the buffer. Tags are included in the Trace
// when the buffer is promoted. It is safe for concurrent use.
func (b *Buffer) Tag(key, value string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tags == nil {
		b.tags = make(map[string]string)
	}
	b.tags[key] = value
}

// Tags returns a copy of the current tags. It is safe for concurrent use.
func (b *Buffer) Tags() map[string]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.tags) == 0 {
		return nil
	}
	cp := make(map[string]string, len(b.tags))
	for k, v := range b.tags {
		cp[k] = v
	}
	return cp
}

// NewBuffer creates a Buffer with the given entry limit. A limit of 0 means
// unlimited (the same as using &Buffer{} directly).
func NewBuffer(limit int) *Buffer {
	if limit < 0 {
		limit = 0
	}
	return &Buffer{limit: limit}
}

// Append adds an entry to the buffer. It is safe for concurrent use.
func (b *Buffer) Append(e Entry) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.limit <= 0 {
		// unlimited mode — original behaviour
		b.entries = append(b.entries, e)
		return
	}

	b.total++
	headSize := b.limit / 2
	tailSize := b.limit - headSize

	if len(b.head) < headSize {
		b.head = append(b.head, e)
		return
	}

	// head is full — add to tail ring
	if len(b.tail) < tailSize {
		b.tail = append(b.tail, e)
	} else {
		b.elided++
		// overwrite oldest tail entry (ring)
		copy(b.tail, b.tail[1:])
		b.tail[tailSize-1] = e
	}
}

// Entries returns a copy of the current entries. It is safe for concurrent use.
// When a limit is active and entries were elided, a synthetic entry is inserted
// between the head and tail portions indicating how many entries were dropped.
func (b *Buffer) Entries() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.limit <= 0 {
		cp := make([]Entry, len(b.entries))
		copy(cp, b.entries)
		return cp
	}

	size := len(b.head) + len(b.tail)
	if b.elided > 0 {
		size++ // synthetic entry
	}
	cp := make([]Entry, 0, size)
	cp = append(cp, b.head...)
	if b.elided > 0 {
		cp = append(cp, Entry{
			Time:    time.Now(),
			Level:   "WARN",
			Message: fmt.Sprintf("promolog: %d log entries elided (buffer limit %d)", b.elided, b.limit),
		})
	}
	cp = append(cp, b.tail...)
	return cp
}

// Snapshot returns a copy of the current entries. It is safe for concurrent use.
//
// Deprecated: Use Entries instead.
func (b *Buffer) Snapshot() []Entry {
	return b.Entries()
}

type bufferKey struct{}

// NewBufferContext returns a new context with an empty, unlimited Buffer
// attached. For a size-limited buffer, use NewBufferContextWithLimit.
func NewBufferContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, bufferKey{}, &Buffer{})
}

// NewBufferContextWithLimit returns a new context with a size-limited Buffer.
// The limit caps the number of entries kept. When the limit is exceeded the
// buffer retains the first and last entries and inserts a synthetic entry
// noting how many middle entries were elided. A limit of 0 means unlimited.
func NewBufferContextWithLimit(ctx context.Context, limit int) context.Context {
	return context.WithValue(ctx, bufferKey{}, NewBuffer(limit))
}

// GetBuffer retrieves the per-request Buffer from the context, or nil.
func GetBuffer(ctx context.Context) *Buffer {
	buf, _ := ctx.Value(bufferKey{}).(*Buffer)
	return buf
}

// Trace contains all the information captured when a request is promoted.
// ErrorChain is optional and may be empty for non-error promotions.
type Trace struct {
	RequestID  string
	ErrorChain string
	StatusCode int
	Route      string
	Method     string
	UserAgent  string
	RemoteIP   string
	UserID     string
	Tags       map[string]string
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
	Tags       map[string]string
	CreatedAt  time.Time
}

// TraceFilter holds all filter parameters for ListTraces.
type TraceFilter struct {
	Q       string
	Status  string
	Method  string
	Tags    map[string]string // filter traces by tag key-value pairs
	Sort    string
	Dir     string
	Page    int
	PerPage int
}

// FilterOptions holds distinct values available for filter dropdowns.
type FilterOptions struct {
	StatusCodes []int
	Methods     []string
	TagKeys     []string // distinct tag keys across all traces
	RemoteIPs   []string
	Routes      []string
	UserIDs     []string
	Tags        map[string][]string // distinct values per tag key
}

// Storer defines the interface for trace persistence. Useful for mocking in tests.
type Storer interface {
	InitSchema() error
	SetOnPromote(fn func(TraceSummary))
	Promote(ctx context.Context, trace Trace) error
	PromoteAt(ctx context.Context, trace Trace, createdAt time.Time) error
	Get(ctx context.Context, requestID string) (*Trace, error)
	ListTraces(ctx context.Context, f TraceFilter) ([]TraceSummary, int, error)
	AvailableFilters(ctx context.Context, f TraceFilter) (FilterOptions, error)
	DeleteTrace(ctx context.Context, requestID string) error
	StartCleanup(ctx context.Context, ttl time.Duration, interval time.Duration)
}
