package promolog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStorer is a minimal Storer for testing promotion.
type mockStorer struct {
	mu     sync.Mutex
	traces []Trace
}

func (m *mockStorer) InitSchema() error                         { return nil }
func (m *mockStorer) SetOnPromote(_ func(TraceSummary))         {}
func (m *mockStorer) Get(_ context.Context, _ string) (*Trace, error) {
	return nil, nil
}
func (m *mockStorer) ListTraces(_ context.Context, _ TraceFilter) ([]TraceSummary, int, error) {
	return nil, 0, nil
}
func (m *mockStorer) AvailableFilters(_ context.Context, _ TraceFilter) (FilterOptions, error) {
	return FilterOptions{}, nil
}
func (m *mockStorer) DeleteTrace(_ context.Context, _ string) error { return nil }
func (m *mockStorer) StartCleanup(_ context.Context, _ time.Duration, _ time.Duration) {
}
func (m *mockStorer) Promote(_ context.Context, trace Trace) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.traces = append(m.traces, trace)
	return nil
}
func (m *mockStorer) PromoteAt(_ context.Context, trace Trace, _ time.Time) error {
	return m.Promote(context.Background(), trace)
}

func (m *mockStorer) promoted() []Trace {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]Trace, len(m.traces))
	copy(cp, m.traces)
	return cp
}

// --- responseWriter tests ---

func TestResponseWriter_CapturesWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec, statusCode: http.StatusOK}
	rw.WriteHeader(http.StatusNotFound)
	assert.Equal(t, http.StatusNotFound, rw.statusCode)
	assert.True(t, rw.written)
}

func TestResponseWriter_DefaultsToOKOnWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec, statusCode: http.StatusOK}
	_, err := rw.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rw.statusCode)
	assert.True(t, rw.written)
}

func TestResponseWriter_FirstWriteHeaderWins(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec, statusCode: http.StatusOK}
	rw.WriteHeader(http.StatusBadRequest)
	rw.WriteHeader(http.StatusInternalServerError)
	assert.Equal(t, http.StatusBadRequest, rw.statusCode)
}

func TestResponseWriter_Unwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec}
	assert.Equal(t, rec, rw.Unwrap())
}

// --- AutoPromoteMiddleware tests ---

func newTestStack(store Storer, policies []PromotionPolicy, handler http.HandlerFunc) http.Handler {
	return CorrelationMiddleware(
		AutoPromoteMiddleware(store, policies...)(handler),
	)
}

func TestAutoPromote_PromotesOnServerError(t *testing.T) {
	store := &mockStorer{}
	stack := newTestStack(store, []PromotionPolicy{StatusPolicy(500)}, func(w http.ResponseWriter, r *http.Request) {
		buf := GetBuffer(r.Context())
		buf.Append(Entry{Level: "ERROR", Message: "db connection failed"})
		w.WriteHeader(http.StatusInternalServerError)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/users", http.NoBody)
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	traces := store.promoted()
	require.Len(t, traces, 1)
	assert.Equal(t, http.StatusInternalServerError, traces[0].StatusCode)
	assert.Equal(t, "/api/users", traces[0].Route)
	assert.Equal(t, http.MethodPost, traces[0].Method)
	require.Len(t, traces[0].Entries, 1)
	assert.Equal(t, "db connection failed", traces[0].Entries[0].Message)
}

func TestAutoPromote_DoesNotPromoteOnSuccess(t *testing.T) {
	store := &mockStorer{}
	stack := newTestStack(store, []PromotionPolicy{StatusPolicy(500)}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	assert.Empty(t, store.promoted())
}

func TestAutoPromote_PromotesOnMatchingRoutePolicy(t *testing.T) {
	store := &mockStorer{}
	policies := []PromotionPolicy{
		RoutePolicy("/api/*", func(code int) bool { return code >= 400 }),
	}
	stack := newTestStack(store, policies, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/orders", http.NoBody)
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	traces := store.promoted()
	require.Len(t, traces, 1)
	assert.Equal(t, http.StatusBadRequest, traces[0].StatusCode)
}

func TestAutoPromote_DoesNotPromoteNonMatchingRoute(t *testing.T) {
	store := &mockStorer{}
	policies := []PromotionPolicy{
		RoutePolicy("/api/*", func(code int) bool { return code >= 400 }),
	}
	stack := newTestStack(store, policies, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	req := httptest.NewRequest(http.MethodGet, "/health", http.NoBody)
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	assert.Empty(t, store.promoted())
}

func TestAutoPromote_FirstMatchingPolicyWins(t *testing.T) {
	store := &mockStorer{}
	policies := []PromotionPolicy{
		StatusPolicy(500),
		StatusPolicy(400), // also matches, but first wins — only one promotion
	}
	stack := newTestStack(store, policies, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	// Only one promotion despite two matching policies.
	assert.Len(t, store.promoted(), 1)
}

func TestAutoPromote_NoPolicies_NeverPromotes(t *testing.T) {
	store := &mockStorer{}
	stack := newTestStack(store, nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	assert.Empty(t, store.promoted())
}

func TestAutoPromote_CapturesRequestMetadata(t *testing.T) {
	store := &mockStorer{}
	stack := newTestStack(store, []PromotionPolicy{StatusPolicy(500)}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	req := httptest.NewRequest(http.MethodPut, "/api/items/42", http.NoBody)
	req.Header.Set("User-Agent", "test-agent/1.0")
	req.RemoteAddr = "192.168.1.100:54321"
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	traces := store.promoted()
	require.Len(t, traces, 1)
	tr := traces[0]
	assert.Equal(t, http.StatusBadGateway, tr.StatusCode)
	assert.Equal(t, "/api/items/42", tr.Route)
	assert.Equal(t, http.MethodPut, tr.Method)
	assert.Equal(t, "test-agent/1.0", tr.UserAgent)
	assert.Equal(t, "192.168.1.100:54321", tr.RemoteIP)
	assert.NotEmpty(t, tr.RequestID)
	assert.False(t, tr.CreatedAt.IsZero())
}

func TestAutoPromote_ImplicitOKWhenNoWriteHeader(t *testing.T) {
	store := &mockStorer{}
	stack := newTestStack(store, []PromotionPolicy{StatusPolicy(500)}, func(w http.ResponseWriter, r *http.Request) {
		// No explicit WriteHeader — Go defaults to 200.
		_, _ = w.Write([]byte("ok"))
	})

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	assert.Empty(t, store.promoted())
}

func TestAutoPromote_WithoutCorrelationMiddleware_NoPromotion(t *testing.T) {
	store := &mockStorer{}
	// Use AutoPromoteMiddleware without CorrelationMiddleware.
	handler := AutoPromoteMiddleware(store, StatusPolicy(500))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// No request ID / buffer in context — should gracefully skip.
	assert.Empty(t, store.promoted())
}
