package promolog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// GetRequestID retrieves the request ID from the context, or returns
// an empty string if none is set.
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(RequestIDKey).(string); ok {
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
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = generateID()
		}
		w.Header().Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), RequestIDKey, requestID)
		if bufferLimit > 0 {
			ctx = NewBufferContextWithLimit(ctx, bufferLimit)
		} else {
			ctx = NewBufferContext(ctx)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
