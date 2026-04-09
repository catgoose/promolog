package promolog

import (
	"bufio"
	"net"
	"net/http"
	"time"
)

// responseWriter wraps http.ResponseWriter to capture the status code written
// by the downstream handler. It is never returned directly to downstream
// handlers — one of the wrap* variants below is returned instead so that the
// exported wrapper's method set matches the optional interfaces supported by
// the underlying writer (http.Flusher, http.Hijacker).
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.statusCode = http.StatusOK
		rw.written = true
	}
	return rw.ResponseWriter.Write(b)
}

// Unwrap returns the underlying ResponseWriter, allowing middleware further up
// the chain to access the original writer (e.g. for http.Flusher).
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// The rw* variants below preserve the exact optional-interface method set of
// the wrapped writer. Each embeds *responseWriter (so WriteHeader/Write/Unwrap
// are inherited) and adds methods for the optional interfaces supported by the
// underlying writer. Defining these methods unconditionally on responseWriter
// itself would cause false-positive type assertions — e.g. a handler would
// think the writer implements http.Flusher even when the underlying writer
// does not, and calling Flush would panic or silently no-op.

// rwFlusher forwards http.Flusher.
type rwFlusher struct {
	*responseWriter
}

func (r rwFlusher) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// rwHijacker forwards http.Hijacker.
type rwHijacker struct {
	*responseWriter
}

func (r rwHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.ResponseWriter.(http.Hijacker).Hijack()
}

// rwFlushHijacker forwards both http.Flusher and http.Hijacker.
type rwFlushHijacker struct {
	*responseWriter
}

func (r rwFlushHijacker) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r rwFlushHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.ResponseWriter.(http.Hijacker).Hijack()
}

// wrapResponseWriter returns an http.ResponseWriter whose method set matches
// the optional interfaces (http.Flusher, http.Hijacker) supported by w. The
// returned wrapper shares state with base, so callers can read base.statusCode
// after the handler has returned.
func wrapResponseWriter(base *responseWriter) http.ResponseWriter {
	_, isFlusher := base.ResponseWriter.(http.Flusher)
	_, isHijacker := base.ResponseWriter.(http.Hijacker)
	switch {
	case isFlusher && isHijacker:
		return rwFlushHijacker{responseWriter: base}
	case isFlusher:
		return rwFlusher{responseWriter: base}
	case isHijacker:
		return rwHijacker{responseWriter: base}
	default:
		return base
	}
}

// AutoPromoteMiddleware returns middleware that captures the response status
// code and evaluates the given PromotionPolicy values after the downstream
// handler returns. If any policy matches, the request's buffer is promoted to
// the store automatically — no manual Promote call is needed.
//
// This middleware must be applied after CorrelationMiddleware so that the
// request context contains both a request ID and a Buffer.
//
// Manual promotion (calling store.Promote directly) remains available as an
// escape hatch for cases not covered by policies.
//
// Usage:
//
//	policies := []promolog.PromotionPolicy{
//	    promolog.StatusPolicy(500),
//	}
//	mux := http.NewServeMux()
//	handler := promolog.CorrelationMiddleware(
//	    promolog.AutoPromoteMiddleware(store, policies...)(mux),
//	)
func AutoPromoteMiddleware(store Storer, policies ...PromotionPolicy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			base := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(wrapResponseWriter(base), r)

			// Check if any policy triggers promotion.
			for _, p := range policies {
				if p.Predicate(r, base.statusCode) {
					promoteFromRequest(store, r, base.statusCode)
					return
				}
			}
		})
	}
}

// promoteFromRequest builds a Trace from the request context and promotes it.
func promoteFromRequest(store Storer, r *http.Request, statusCode int) {
	ctx := r.Context()
	requestID := GetRequestID(ctx)
	if requestID == "" {
		return
	}

	buf := GetBuffer(ctx)
	if buf == nil {
		return
	}

	trace := Trace{
		RequestID:       requestID,
		ParentRequestID: GetParentRequestID(ctx),
		StatusCode:      statusCode,
		Route:           r.URL.Path,
		Method:          r.Method,
		UserAgent:       r.UserAgent(),
		RemoteIP:        r.RemoteAddr,
		Tags:            buf.Tags(),
		Entries:         buf.Entries(),
		RequestBody:     buf.RequestBody(),
		ResponseBody:    buf.ResponseBody(),
		CreatedAt:       time.Now(),
	}

	// Best-effort promotion; errors are intentionally ignored because the
	// middleware cannot meaningfully surface them to the caller.
	_ = store.Promote(ctx, trace)
}
