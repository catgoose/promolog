package promolog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"
)

type startTimeKeyType struct{}

var startTimeKey = startTimeKeyType{}

// GetRequestStartTime retrieves the request start time from the context, or
// returns the zero time if none is set.
func GetRequestStartTime(ctx context.Context) time.Time {
	if t, ok := ctx.Value(startTimeKey).(time.Time); ok {
		return t
	}
	return time.Time{}
}

// GetRequestDuration returns the elapsed time since the request started. If no
// start time is present in the context it returns 0.
func GetRequestDuration(r *http.Request) time.Duration {
	start := GetRequestStartTime(r.Context())
	if start.IsZero() {
		return 0
	}
	return time.Since(start)
}

// GetRequestID retrieves the request ID from the context, or returns
// an empty string if none is set.
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(RequestIDKey).(string); ok {
		return id
	}
	return ""
}

// GetParentRequestID retrieves the parent request ID from the context, or
// returns an empty string if none is set.
func GetParentRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(parentRequestIDKey).(string); ok {
		return id
	}
	return ""
}

// generateID produces a 32-character hex-encoded random request ID.
func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// CorrelationMiddleware is stdlib HTTP middleware that sets up per-request
// correlation for promolog. It generates a unique request ID (or reuses one
// from the incoming X-Request-ID header), sets the X-Request-ID response
// header, stores the ID in the request context, and initializes a promolog
// Buffer for log capture.
//
// Usage with net/http:
//
//	mux := http.NewServeMux()
//	http.ListenAndServe(":8080", promolog.CorrelationMiddleware(mux))
//
// Usage with Echo:
//
//	e.Use(echo.WrapMiddleware(promolog.CorrelationMiddleware))
func CorrelationMiddleware(next http.Handler) http.Handler {
	return correlationMiddleware(next, 0)
}

// CorrelationMiddlewareWithLimit works like CorrelationMiddleware but
// initialises the per-request Buffer with the given entry limit. A limit of 0
// means unlimited.
func CorrelationMiddlewareWithLimit(limit int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return correlationMiddleware(next, limit)
	}
}

func correlationMiddleware(next http.Handler, bufferLimit int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		incomingID := r.Header.Get("X-Request-ID")
		parentID := r.Header.Get("X-Parent-Request-ID")

		requestID := incomingID
		if requestID == "" {
			requestID = generateID()
		}

		// If a parent header was explicitly provided, use it. Otherwise,
		// when an incoming X-Request-ID was present, treat it as the parent
		// and generate a fresh child ID to represent this service's span.
		if parentID == "" && incomingID != "" {
			parentID = incomingID
			requestID = generateID()
		}

		w.Header().Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), RequestIDKey, requestID)
		if parentID != "" {
			ctx = context.WithValue(ctx, parentRequestIDKey, parentID)
		}
		ctx = context.WithValue(ctx, startTimeKey, time.Now())
		if bufferLimit > 0 {
			ctx = NewBufferContextWithLimit(ctx, bufferLimit)
		} else {
			ctx = NewBufferContext(ctx)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// correlationRoundTripper is an http.RoundTripper that propagates the request
// ID from the request context to outgoing HTTP requests via the X-Request-ID
// header.
type correlationRoundTripper struct {
	base http.RoundTripper
}

// CorrelationTransport returns an http.RoundTripper that reads the request ID
// from the outgoing request's context and sets it as the X-Request-ID header.
// If no request ID is present in the context the request is passed through
// unmodified. When base is nil, http.DefaultTransport is used.
func CorrelationTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &correlationRoundTripper{base: base}
}

func (t *correlationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	id := GetRequestID(req.Context())
	if id != "" {
		// Clone the request to avoid mutating the caller's headers.
		r2 := req.Clone(req.Context())
		r2.Header.Set("X-Request-ID", id)
		return t.base.RoundTrip(r2)
	}
	return t.base.RoundTrip(req)
}

// NewCorrelatedClient returns a shallow copy of base (or http.DefaultClient
// when base is nil) whose transport propagates request IDs via the
// X-Request-ID header. The original client is not modified.
func NewCorrelatedClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	cp := *base
	cp.Transport = CorrelationTransport(base.Transport)
	return &cp
}
