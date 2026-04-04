package promolog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCorrelationMiddleware_GeneratesRequestID(t *testing.T) {
	handler := CorrelationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := GetRequestID(r.Context())
		assert.NotEmpty(t, id)
		assert.Len(t, id, 32) // 16 bytes hex-encoded

		buf := GetBuffer(r.Context())
		assert.NotNil(t, buf)

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotEmpty(t, rec.Header().Get("X-Request-ID"))
	assert.Len(t, rec.Header().Get("X-Request-ID"), 32)
}

func TestCorrelationMiddleware_IncomingRequestID_BecomesParent(t *testing.T) {
	incoming := "abc-from-load-balancer-123"

	var capturedID, capturedParent string
	handler := CorrelationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = GetRequestID(r.Context())
		capturedParent = GetParentRequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("X-Request-ID", incoming)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The incoming ID becomes the parent; a new child ID is generated.
	assert.Equal(t, incoming, capturedParent)
	assert.NotEqual(t, incoming, capturedID)
	assert.Len(t, capturedID, 32)
	assert.NotEqual(t, incoming, rec.Header().Get("X-Request-ID"))
}

func TestCorrelationMiddleware_InitializesBuffer(t *testing.T) {
	handler := CorrelationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := GetBuffer(r.Context())
		require.NotNil(t, buf)

		buf.Append(Entry{Level: "INFO", Message: "test"})
		assert.Len(t, buf.Snapshot(), 1)

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestCorrelationMiddleware_CallsNext(t *testing.T) {
	called := false
	handler := CorrelationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusTeapot, rec.Code)
}

func TestGetRequestID_EmptyWithoutMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	assert.Empty(t, GetRequestID(req.Context()))
}

func TestGenerateID_Length(t *testing.T) {
	id := generateID()
	assert.Len(t, id, 32)
}

func TestGenerateID_Unique(t *testing.T) {
	ids := make(map[string]bool)
	for range 100 {
		id := generateID()
		assert.False(t, ids[id], "duplicate ID generated")
		ids[id] = true
	}
}

func TestCorrelationMiddleware_ExplicitParentHeader(t *testing.T) {
	var capturedID, capturedParent string
	handler := CorrelationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = GetRequestID(r.Context())
		capturedParent = GetParentRequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("X-Parent-Request-ID", "parent-from-gateway")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "parent-from-gateway", capturedParent)
	assert.Len(t, capturedID, 32) // new generated ID
	assert.Len(t, rec.Header().Get("X-Request-ID"), 32)
}

func TestCorrelationMiddleware_NoParentWithoutIncomingID(t *testing.T) {
	var capturedParent string
	handler := CorrelationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedParent = GetParentRequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Empty(t, capturedParent)
}

func TestGetParentRequestID_EmptyWithoutMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	assert.Empty(t, GetParentRequestID(req.Context()))
}

func TestCorrelationTransport_PropagatesRequestID(t *testing.T) {
	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: CorrelationTransport(nil)}
	ctx := context.WithValue(context.Background(), RequestIDKey, "test-req-id")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "test-req-id", received)
}

func TestCorrelationTransport_NoIDPassesThrough(t *testing.T) {
	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: CorrelationTransport(nil)}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, http.NoBody)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Empty(t, received)
}

func TestCorrelationTransport_NilBaseUsesDefault(t *testing.T) {
	rt := CorrelationTransport(nil)
	crt, ok := rt.(*correlationRoundTripper)
	require.True(t, ok)
	assert.Equal(t, http.DefaultTransport, crt.base)
}

func TestNewCorrelatedClient_NilBaseUsesDefault(t *testing.T) {
	client := NewCorrelatedClient(nil)
	require.NotNil(t, client)
	_, ok := client.Transport.(*correlationRoundTripper)
	assert.True(t, ok)
}

func TestNewCorrelatedClient_PreservesTimeout(t *testing.T) {
	base := &http.Client{Timeout: 42 * time.Second}
	client := NewCorrelatedClient(base)
	assert.Equal(t, 42*time.Second, client.Timeout)
	// Original client should not be modified.
	assert.Nil(t, base.Transport)
}

func TestNewCorrelatedClient_PropagatesID(t *testing.T) {
	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewCorrelatedClient(nil)
	ctx := context.WithValue(context.Background(), RequestIDKey, "correlated-id")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "correlated-id", received)
}
