package promolog

import (
	"net/http"
	"time"
)

// responseWriter wraps http.ResponseWriter to capture the status code written
// by the downstream handler.
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
			rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(rw, r)

			// Check if any policy triggers promotion.
			for _, p := range policies {
				if p.Predicate(r, rw.statusCode) {
					promoteFromRequest(store, r, rw.statusCode)
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
