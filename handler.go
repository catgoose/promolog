package promolog

import (
	"context"
	"log/slog"
)

// Handler is a slog.Handler that captures log records into a per-request
// Buffer when the record is associated with a request ID.
type Handler struct {
	inner     slog.Handler
	requestID string
	attrs     []slog.Attr
}

// NewHandler wraps an existing slog.Handler so that every record with a
// request_id attribute is also buffered per-request for promote-on-error.
func NewHandler(inner slog.Handler) *Handler {
	return &Handler{inner: inner}
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
			attrs := make(map[string]string)
			for _, a := range h.attrs {
				if a.Key != "request_id" {
					attrs[a.Key] = a.Value.String()
				}
			}
			r.Attrs(func(a slog.Attr) bool {
				if a.Key != "request_id" {
					attrs[a.Key] = a.Value.String()
				}
				return true
			})
			var entryAttrs map[string]string
			if len(attrs) > 0 {
				entryAttrs = attrs
			}
			buf.Append(Entry{
				Time:    r.Time,
				Level:   r.Level.String(),
				Message: r.Message,
				Attrs:   entryAttrs,
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
		inner:     h.inner.WithAttrs(attrs),
		requestID: reqID,
		attrs:     append(cloneAttrs(h.attrs), attrs...),
	}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		inner:     h.inner.WithGroup(name),
		requestID: h.requestID,
		attrs:     cloneAttrs(h.attrs),
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
