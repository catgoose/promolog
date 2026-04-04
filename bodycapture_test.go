package promolog

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBodyCaptureMiddleware_CapturesRequestBody(t *testing.T) {
	store := &mockStorer{}
	body := `{"username":"alice","password":"secret"}`

	stack := CorrelationMiddleware(
		BodyCaptureMiddleware()(
			AutoPromoteMiddleware(store, StatusPolicy(500))(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Handler should still be able to read the body.
					b, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					assert.Equal(t, body, string(b))
					w.WriteHeader(http.StatusInternalServerError)
				}),
			),
		),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	traces := store.promoted()
	require.Len(t, traces, 1)
	assert.Equal(t, body, traces[0].RequestBody)
}

func TestBodyCaptureMiddleware_CapturesResponseBody(t *testing.T) {
	store := &mockStorer{}
	respBody := `{"error":"internal server error"}`

	stack := CorrelationMiddleware(
		BodyCaptureMiddleware()(
			AutoPromoteMiddleware(store, StatusPolicy(500))(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(respBody))
				}),
			),
		),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/data", http.NoBody)
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	traces := store.promoted()
	require.Len(t, traces, 1)
	assert.Equal(t, respBody, traces[0].ResponseBody)
}

func TestBodyCaptureMiddleware_TruncatesLargeBody(t *testing.T) {
	store := &mockStorer{}
	largeBody := strings.Repeat("x", 200)

	stack := CorrelationMiddleware(
		BodyCaptureMiddleware(WithMaxBodySize(50))(
			AutoPromoteMiddleware(store, StatusPolicy(500))(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}),
			),
		),
	)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(largeBody))
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	traces := store.promoted()
	require.Len(t, traces, 1)
	assert.Len(t, traces[0].RequestBody, 50)
}

func TestBodyCaptureMiddleware_TruncatesLargeResponseBody(t *testing.T) {
	store := &mockStorer{}
	largeResp := strings.Repeat("y", 200)

	stack := CorrelationMiddleware(
		BodyCaptureMiddleware(WithMaxBodySize(50))(
			AutoPromoteMiddleware(store, StatusPolicy(500))(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(largeResp))
				}),
			),
		),
	)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	traces := store.promoted()
	require.Len(t, traces, 1)
	assert.Len(t, traces[0].ResponseBody, 50)
}

func TestBodyCaptureMiddleware_RedactorApplied(t *testing.T) {
	store := &mockStorer{}
	body := `{"password":"secret123"}`

	redactor := func(b []byte) []byte {
		return bytes.ReplaceAll(b, []byte("secret123"), []byte("[REDACTED]"))
	}

	stack := CorrelationMiddleware(
		BodyCaptureMiddleware(WithRedactor(redactor))(
			AutoPromoteMiddleware(store, StatusPolicy(500))(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"token":"secret123"}`))
				}),
			),
		),
	)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	traces := store.promoted()
	require.Len(t, traces, 1)
	assert.Contains(t, traces[0].RequestBody, "[REDACTED]")
	assert.NotContains(t, traces[0].RequestBody, "secret123")
	assert.Contains(t, traces[0].ResponseBody, "[REDACTED]")
	assert.NotContains(t, traces[0].ResponseBody, "secret123")
}

func TestBodyCaptureMiddleware_NoBuffer_PassesThrough(t *testing.T) {
	called := false
	mw := BodyCaptureMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestBodyCaptureMiddleware_EmptyBody(t *testing.T) {
	store := &mockStorer{}

	stack := CorrelationMiddleware(
		BodyCaptureMiddleware()(
			AutoPromoteMiddleware(store, StatusPolicy(500))(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}),
			),
		),
	)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	traces := store.promoted()
	require.Len(t, traces, 1)
	assert.Empty(t, traces[0].RequestBody)
	assert.Empty(t, traces[0].ResponseBody)
}

func TestBodyCaptureMiddleware_NoPromotion_NoBodiesStored(t *testing.T) {
	store := &mockStorer{}

	stack := CorrelationMiddleware(
		BodyCaptureMiddleware()(
			AutoPromoteMiddleware(store, StatusPolicy(500))(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("ok"))
				}),
			),
		),
	)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("request data"))
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	// No promotion, so no traces.
	assert.Empty(t, store.promoted())
}

func TestBuffer_SetRequestBody_ConcurrentSafe(t *testing.T) {
	buf := &Buffer{}
	done := make(chan struct{})

	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			buf.SetRequestBody("body-a")
		}
	}()
	for i := 0; i < 100; i++ {
		buf.SetRequestBody("body-b")
	}
	<-done

	body := buf.RequestBody()
	assert.True(t, body == "body-a" || body == "body-b")
}

func TestBuffer_SetResponseBody_ConcurrentSafe(t *testing.T) {
	buf := &Buffer{}
	done := make(chan struct{})

	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			buf.SetResponseBody("resp-a")
		}
	}()
	for i := 0; i < 100; i++ {
		buf.SetResponseBody("resp-b")
	}
	<-done

	body := buf.ResponseBody()
	assert.True(t, body == "resp-a" || body == "resp-b")
}

func TestCaptureResponseWriter_Unwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	crw := &captureResponseWriter{
		ResponseWriter: rec,
		maxSize:        1024,
		capBuf:         &bytes.Buffer{},
		promoBuf:       &Buffer{},
	}
	assert.Equal(t, rec, crw.Unwrap())
}
