package promolog

import (
	"net/http"
	"net/http/httptest"
	"testing"

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

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotEmpty(t, rec.Header().Get("X-Request-ID"))
	assert.Len(t, rec.Header().Get("X-Request-ID"), 32)
}

func TestCorrelationMiddleware_ReusesIncomingRequestID(t *testing.T) {
	incoming := "abc-from-load-balancer-123"

	var captured string
	handler := CorrelationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = GetRequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", incoming)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, incoming, captured)
	assert.Equal(t, incoming, rec.Header().Get("X-Request-ID"))
}

func TestCorrelationMiddleware_InitializesBuffer(t *testing.T) {
	handler := CorrelationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := GetBuffer(r.Context())
		require.NotNil(t, buf)

		buf.Append(Entry{Level: "INFO", Message: "test"})
		assert.Len(t, buf.Snapshot(), 1)

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusTeapot, rec.Code)
}

func TestGetRequestID_EmptyWithoutMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
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
