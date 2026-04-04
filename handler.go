package promolog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// HandlerOption configures optional Handler behaviour.
type HandlerOption func(*Handler)

// WithBufferLimit sets a cap on the number of log entries the per-request
// Buffer will retain. When the limit is exceeded the buffer keeps the first
// and last entries and inserts a synthetic entry noting how many middle entries
// were elided. A limit of 0 (the default) means unlimited.
func WithBufferLimit(n int) HandlerOption {
	return func(h *Handler) {
		h.bufferLimit = n
	}
}

// Handler is a slog.Handler that captures log records into a per-request
// Buffer when the record is associated with a request ID.
type Handler struct {
	inner       slog.Handler
	requestID   string
	attrs       []slog.Attr
	bufferLimit int // 0 = unlimited
}

// NewHandler wraps an existing slog.Handler so that every record with a
// request_id attribute is also buffered per-request for promote-on-error.
// Optional HandlerOption values can configure buffer limits and other settings.
func NewHandler(inner slog.Handler, opts ...HandlerOption) *Handler {
	h := &Handler{inner: inner}
	for _, o := range opts {
		o(h)
	}
	return h
}

// BufferLimit returns the buffer entry limit configured on this Handler.
// A value of 0 means unlimited.
func (h *Handler) BufferLimit() int {
	return h.bufferLimit
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	reqID := h.requestID
	if reqID == "" {
		if id, ok := ctx.Value(RequestIDKey).(string); ok {
			reqID = id
		}
	}

	if reqID != "" {
		if buf := GetBuffer(ctx); buf != nil {
			var parts []string
			for _, a := range h.attrs {
				if a.Key != "request_id" {
					parts = append(parts, fmt.Sprintf("%s=%s", a.Key, a.Value.String()))
				}
			}
			r.Attrs(func(a slog.Attr) bool {
				if a.Key != "request_id" {
					parts = append(parts, fmt.Sprintf("%s=%s", a.Key, a.Value.String()))
				}
				return true
			})
			buf.Append(Entry{
				Time:    r.Time,
				Level:   r.Level.String(),
				Message: r.Message,
				Attrs:   strings.Join(parts, " "),
			})
		}
	}

	return h.inner.Handle(ctx, r)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	reqID := h.requestID
	for _, a := range attrs {
		if a.Key == "request_id" {
			reqID = a.Value.String()
			break
		}
	}
	return &Handler{
		inner:       h.inner.WithAttrs(attrs),
		requestID:   reqID,
		attrs:       append(cloneAttrs(h.attrs), attrs...),
		bufferLimit: h.bufferLimit,
	}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		inner:       h.inner.WithGroup(name),
		requestID:   h.requestID,
		attrs:       cloneAttrs(h.attrs),
		bufferLimit: h.bufferLimit,
	}
}

func cloneAttrs(src []slog.Attr) []slog.Attr {
	if len(src) == 0 {
		return nil
	}
	dst := make([]slog.Attr, len(src))
	copy(dst, src)
	return dst
}
