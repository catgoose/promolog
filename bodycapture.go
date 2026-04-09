package promolog

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
)

const defaultMaxBodySize = 64 * 1024 // 64 KiB

// BodyCaptureOption configures the BodyCaptureMiddleware.
type BodyCaptureOption func(*bodyCaptureConfig)

type bodyCaptureConfig struct {
	maxBodySize int
	redactor    func(body []byte) []byte
}

// WithMaxBodySize sets the maximum number of bytes captured from the request
// and response bodies. Bodies larger than this are truncated. The default is
// 64 KiB.
func WithMaxBodySize(n int) BodyCaptureOption {
	return func(c *bodyCaptureConfig) {
		if n > 0 {
			c.maxBodySize = n
		}
	}
}

// WithRedactor registers a function that is applied to captured bodies before
// they are stored in the Buffer. Use this to strip sensitive data such as
// passwords or tokens.
func WithRedactor(fn func(body []byte) []byte) BodyCaptureOption {
	return func(c *bodyCaptureConfig) {
		c.redactor = fn
	}
}

// BodyCaptureMiddleware returns middleware that captures request and response
// bodies into the per-request Buffer. It must be applied after
// CorrelationMiddleware so that the context contains a Buffer.
//
// Usage:
//
//	handler := promolog.CorrelationMiddleware(
//	    promolog.BodyCaptureMiddleware()(
//	        promolog.AutoPromoteMiddleware(store, policies...)(mux),
//	    ),
//	)
func BodyCaptureMiddleware(opts ...BodyCaptureOption) func(http.Handler) http.Handler {
	cfg := bodyCaptureConfig{maxBodySize: defaultMaxBodySize}
	for _, o := range opts {
		o(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := GetBuffer(r.Context())
			if buf == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Capture request body.
			if r.Body != nil && r.Body != http.NoBody {
				limited := io.LimitReader(r.Body, int64(cfg.maxBodySize)+1)
				body, err := io.ReadAll(limited)
				if err == nil && len(body) > 0 {
					captured := body
					if len(captured) > cfg.maxBodySize {
						captured = captured[:cfg.maxBodySize]
					}
					if cfg.redactor != nil {
						captured = cfg.redactor(captured)
					}
					buf.SetRequestBody(string(captured))
				}
				// Re-wrap so downstream handlers can still read the body.
				r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
			}

			// Wrap response writer to capture written bytes. The captured
			// bytes are stored into the Buffer on each Write call so that
			// inner middleware (e.g. AutoPromoteMiddleware) can read them
			// when building the Trace.
			crw := &captureResponseWriter{
				ResponseWriter: w,
				maxSize:        cfg.maxBodySize,
				capBuf:         &bytes.Buffer{},
				promoBuf:       buf,
				redactor:       cfg.redactor,
			}
			next.ServeHTTP(wrapCaptureResponseWriter(crw), r)
		})
	}
}

// captureResponseWriter wraps http.ResponseWriter and copies written bytes
// into a local buffer up to maxSize. After each Write the current captured
// content (with optional redaction) is pushed into the per-request Buffer so
// that inner middleware can access it. It is never handed to downstream
// handlers directly — wrapCaptureResponseWriter picks a variant whose method
// set matches the optional interfaces supported by the underlying writer.
type captureResponseWriter struct {
	http.ResponseWriter
	maxSize  int
	capBuf   *bytes.Buffer // local accumulator
	captured int
	promoBuf *Buffer                  // per-request Buffer
	redactor func(body []byte) []byte // optional redaction hook
}

func (crw *captureResponseWriter) Write(b []byte) (int, error) {
	remaining := crw.maxSize - crw.captured
	if remaining > 0 {
		toCapture := b
		if len(toCapture) > remaining {
			toCapture = toCapture[:remaining]
		}
		crw.capBuf.Write(toCapture)
		crw.captured += len(toCapture)

		// Push into the per-request buffer so inner middleware sees it.
		captured := crw.capBuf.Bytes()
		if crw.redactor != nil {
			captured = crw.redactor(append([]byte(nil), captured...))
		}
		crw.promoBuf.SetResponseBody(string(captured))
	}
	return crw.ResponseWriter.Write(b)
}

func (crw *captureResponseWriter) WriteHeader(code int) {
	crw.ResponseWriter.WriteHeader(code)
}

// Unwrap returns the underlying ResponseWriter, allowing middleware further up
// the chain to access the original writer (e.g. for http.Flusher).
func (crw *captureResponseWriter) Unwrap() http.ResponseWriter {
	return crw.ResponseWriter
}

// The crw* variants below preserve the exact optional-interface method set of
// the wrapped writer, for the same reason documented on responseWriter's
// variants in autopromote.go.

// crwFlusher forwards http.Flusher.
type crwFlusher struct {
	*captureResponseWriter
}

func (r crwFlusher) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// crwHijacker forwards http.Hijacker. Note: once the connection is hijacked,
// any bytes written directly to the raw net.Conn bypass this wrapper's Write
// method, so they will NOT be captured into the per-request Buffer. That is
// an inherent limitation of hijacking — the middleware has no visibility into
// the hijacked stream.
type crwHijacker struct {
	*captureResponseWriter
}

func (r crwHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.ResponseWriter.(http.Hijacker).Hijack()
}

// crwFlushHijacker forwards both http.Flusher and http.Hijacker.
type crwFlushHijacker struct {
	*captureResponseWriter
}

func (r crwFlushHijacker) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack delegates to the underlying writer. See the note on crwHijacker
// about captured-bytes visibility after hijacking.
func (r crwFlushHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.ResponseWriter.(http.Hijacker).Hijack()
}

// wrapCaptureResponseWriter returns an http.ResponseWriter whose method set
// matches the optional interfaces (http.Flusher, http.Hijacker) supported by
// the underlying writer. The returned wrapper shares state with base.
func wrapCaptureResponseWriter(base *captureResponseWriter) http.ResponseWriter {
	_, isFlusher := base.ResponseWriter.(http.Flusher)
	_, isHijacker := base.ResponseWriter.(http.Hijacker)
	switch {
	case isFlusher && isHijacker:
		return crwFlushHijacker{captureResponseWriter: base}
	case isFlusher:
		return crwFlusher{captureResponseWriter: base}
	case isHijacker:
		return crwHijacker{captureResponseWriter: base}
	default:
		return base
	}
}
