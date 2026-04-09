package promolog

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- stub ResponseWriter implementations for interface testing ---

// plainWriter implements only http.ResponseWriter.
type plainWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newPlainWriter() *plainWriter {
	return &plainWriter{header: make(http.Header)}
}

func (p *plainWriter) Header() http.Header         { return p.header }
func (p *plainWriter) Write(b []byte) (int, error) { return p.body.Write(b) }
func (p *plainWriter) WriteHeader(code int)        { p.status = code }

// flusherWriter implements http.ResponseWriter and http.Flusher.
type flusherWriter struct {
	plainWriter
	flushed int
}

func newFlusherWriter() *flusherWriter {
	return &flusherWriter{plainWriter: plainWriter{header: make(http.Header)}}
}

func (f *flusherWriter) Flush() { f.flushed++ }

// hijackerWriter implements http.ResponseWriter and http.Hijacker.
type hijackerWriter struct {
	plainWriter
	hijacked bool
	hijErr   error
}

func newHijackerWriter() *hijackerWriter {
	return &hijackerWriter{plainWriter: plainWriter{header: make(http.Header)}}
}

func (h *hijackerWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	if h.hijErr != nil {
		return nil, nil, h.hijErr
	}
	// Return a sentinel non-nil value so callers can detect delegation.
	return nil, &bufio.ReadWriter{}, nil
}

// flusherHijackerWriter implements http.ResponseWriter, http.Flusher, and
// http.Hijacker.
type flusherHijackerWriter struct {
	plainWriter
	flushed  int
	hijacked bool
}

func newFlusherHijackerWriter() *flusherHijackerWriter {
	return &flusherHijackerWriter{plainWriter: plainWriter{header: make(http.Header)}}
}

func (f *flusherHijackerWriter) Flush() { f.flushed++ }

func (f *flusherHijackerWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	f.hijacked = true
	return nil, &bufio.ReadWriter{}, nil
}

// --- responseWriter (autopromote) interface preservation tests ---

func TestAutoPromoteWrapper_PlainWriter_NoOptionalInterfaces(t *testing.T) {
	base := &responseWriter{ResponseWriter: newPlainWriter(), statusCode: http.StatusOK}
	w := wrapResponseWriter(base)
	_, isFlusher := w.(http.Flusher)
	_, isHijacker := w.(http.Hijacker)
	assert.False(t, isFlusher, "plain writer must not expose http.Flusher")
	assert.False(t, isHijacker, "plain writer must not expose http.Hijacker")
}

func TestAutoPromoteWrapper_PreservesFlusher(t *testing.T) {
	inner := newFlusherWriter()
	base := &responseWriter{ResponseWriter: inner, statusCode: http.StatusOK}
	w := wrapResponseWriter(base)

	f, ok := w.(http.Flusher)
	require.True(t, ok, "wrapper must implement http.Flusher when underlying writer does")
	f.Flush()
	assert.Equal(t, 1, inner.flushed, "Flush must delegate to underlying writer")

	_, isHijacker := w.(http.Hijacker)
	assert.False(t, isHijacker, "must not expose http.Hijacker when underlying writer does not")
}

func TestAutoPromoteWrapper_PreservesHijacker(t *testing.T) {
	inner := newHijackerWriter()
	base := &responseWriter{ResponseWriter: inner, statusCode: http.StatusOK}
	w := wrapResponseWriter(base)

	h, ok := w.(http.Hijacker)
	require.True(t, ok, "wrapper must implement http.Hijacker when underlying writer does")
	_, _, err := h.Hijack()
	require.NoError(t, err)
	assert.True(t, inner.hijacked, "Hijack must delegate to underlying writer")

	_, isFlusher := w.(http.Flusher)
	assert.False(t, isFlusher, "must not expose http.Flusher when underlying writer does not")
}

func TestAutoPromoteWrapper_PreservesFlusherAndHijacker(t *testing.T) {
	inner := newFlusherHijackerWriter()
	base := &responseWriter{ResponseWriter: inner, statusCode: http.StatusOK}
	w := wrapResponseWriter(base)

	f, ok := w.(http.Flusher)
	require.True(t, ok)
	f.Flush()
	assert.Equal(t, 1, inner.flushed)

	h, ok := w.(http.Hijacker)
	require.True(t, ok)
	_, _, err := h.Hijack()
	require.NoError(t, err)
	assert.True(t, inner.hijacked)
}

func TestAutoPromoteWrapper_HijackErrorPropagates(t *testing.T) {
	inner := newHijackerWriter()
	inner.hijErr = errors.New("boom")
	base := &responseWriter{ResponseWriter: inner, statusCode: http.StatusOK}
	w := wrapResponseWriter(base)

	h, ok := w.(http.Hijacker)
	require.True(t, ok)
	_, _, err := h.Hijack()
	assert.EqualError(t, err, "boom")
}

func TestAutoPromoteWrapper_HandlerCanTypeAssertFlusher(t *testing.T) {
	// End-to-end: run AutoPromoteMiddleware and verify the handler sees a
	// working http.Flusher when the underlying writer supports it.
	store := &mockStorer{}
	var sawFlusher bool
	stack := newTestStack(store, nil, func(w http.ResponseWriter, _ *http.Request) {
		_, sawFlusher = w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
	})
	// httptest.ResponseRecorder does not implement http.Flusher, so wrap it
	// into a stub that does.
	inner := newFlusherWriter()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	stack.ServeHTTP(inner, req)
	assert.True(t, sawFlusher, "handler must observe a Flusher through the wrapper")
}

// --- captureResponseWriter (bodycapture) interface preservation tests ---

func newCRW(inner http.ResponseWriter) *captureResponseWriter {
	return &captureResponseWriter{
		ResponseWriter: inner,
		maxSize:        1024,
		capBuf:         &bytes.Buffer{},
		promoBuf:       &Buffer{},
	}
}

func TestBodyCaptureWrapper_PlainWriter_NoOptionalInterfaces(t *testing.T) {
	w := wrapCaptureResponseWriter(newCRW(newPlainWriter()))
	_, isFlusher := w.(http.Flusher)
	_, isHijacker := w.(http.Hijacker)
	assert.False(t, isFlusher)
	assert.False(t, isHijacker)
}

func TestBodyCaptureWrapper_PreservesFlusher(t *testing.T) {
	inner := newFlusherWriter()
	w := wrapCaptureResponseWriter(newCRW(inner))

	f, ok := w.(http.Flusher)
	require.True(t, ok, "wrapper must implement http.Flusher when underlying writer does")
	f.Flush()
	assert.Equal(t, 1, inner.flushed)

	_, isHijacker := w.(http.Hijacker)
	assert.False(t, isHijacker)
}

func TestBodyCaptureWrapper_PreservesHijacker(t *testing.T) {
	inner := newHijackerWriter()
	w := wrapCaptureResponseWriter(newCRW(inner))

	h, ok := w.(http.Hijacker)
	require.True(t, ok, "wrapper must implement http.Hijacker when underlying writer does")
	_, _, err := h.Hijack()
	require.NoError(t, err)
	assert.True(t, inner.hijacked)

	_, isFlusher := w.(http.Flusher)
	assert.False(t, isFlusher)
}

func TestBodyCaptureWrapper_PreservesFlusherAndHijacker(t *testing.T) {
	inner := newFlusherHijackerWriter()
	w := wrapCaptureResponseWriter(newCRW(inner))

	f, ok := w.(http.Flusher)
	require.True(t, ok)
	f.Flush()
	assert.Equal(t, 1, inner.flushed)

	h, ok := w.(http.Hijacker)
	require.True(t, ok)
	_, _, err := h.Hijack()
	require.NoError(t, err)
	assert.True(t, inner.hijacked)
}

func TestBodyCaptureWrapper_HandlerCanTypeAssertHijacker(t *testing.T) {
	// End-to-end: run BodyCaptureMiddleware with CorrelationMiddleware and
	// verify the handler sees an http.Hijacker when the underlying writer
	// supports it.
	var sawHijacker bool
	stack := CorrelationMiddleware(
		BodyCaptureMiddleware()(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, sawHijacker = w.(http.Hijacker)
				w.WriteHeader(http.StatusOK)
			}),
		),
	)
	inner := newHijackerWriter()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	stack.ServeHTTP(inner, req)
	assert.True(t, sawHijacker, "handler must observe a Hijacker through the wrapper")
}
