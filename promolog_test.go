package promolog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Buffer context tests ---

func TestNewBufferContext_GetBuffer_Roundtrip(t *testing.T) {
	ctx := NewBufferContext(context.Background())
	buf := GetBuffer(ctx)
	require.NotNil(t, buf)
	assert.Empty(t, buf.Entries())
}

func TestGetBuffer_PlainContext_ReturnsNil(t *testing.T) {
	buf := GetBuffer(context.Background())
	assert.Nil(t, buf)
}

func TestBuffer_Append_And_Snapshot(t *testing.T) {
	buf := &Buffer{}
	e := Entry{Time: time.Now(), Level: "INFO", Message: "hello"}
	buf.Append(e)
	buf.Append(e)

	snap := buf.Entries()
	assert.Len(t, snap, 2)
	// Entries() is a copy — mutating it doesn't affect the buffer.
	snap[0].Message = "mutated"
	assert.Equal(t, "hello", buf.Entries()[0].Message)
}

// --- Buffer concurrency tests ---

func TestBuffer_ConcurrentAppendAndSnapshot(t *testing.T) {
	buf := &Buffer{}
	const goroutines = 10
	const perGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			for i := range perGoroutine {
				buf.Append(Entry{
					Level:   "INFO",
					Message: fmt.Sprintf("g%d-i%d", id, i),
				})
			}
		}(g)
	}
	wg.Wait()

	snap := buf.Snapshot()
	assert.Len(t, snap, goroutines*perGoroutine)
}

func TestBuffer_Snapshot_Empty(t *testing.T) {
	buf := &Buffer{}
	snap := buf.Snapshot()
	assert.NotNil(t, snap)
	assert.Empty(t, snap)
}

// --- Handler tests ---

type discardHandler struct{ handled bool }

func (d *discardHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (d *discardHandler) Handle(_ context.Context, _ slog.Record) error {
	d.handled = true
	return nil
}
func (d *discardHandler) WithAttrs(_ []slog.Attr) slog.Handler { return d }
func (d *discardHandler) WithGroup(_ string) slog.Handler       { return d }

func TestHandler_CapturesEntries_WhenRequestIDInContext(t *testing.T) {
	inner := &discardHandler{}
	h := NewHandler(inner)

	ctx := context.WithValue(context.Background(), RequestIDKey, "req-123")
	ctx = NewBufferContext(ctx)

	rec := slog.NewRecord(time.Now(), slog.LevelError, "something broke", 0)
	rec.AddAttrs(slog.String("component", "auth"))

	require.NoError(t, h.Handle(ctx, rec))

	buf := GetBuffer(ctx)
	require.Len(t, buf.Entries(), 1)
	assert.Equal(t, "something broke", buf.Entries()[0].Message)
	assert.Contains(t, buf.Entries()[0].Attrs, "component=auth")
}

func TestHandler_DoesNotCapture_WhenNoRequestID(t *testing.T) {
	h := NewHandler(&discardHandler{})
	ctx := NewBufferContext(context.Background())

	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "no request id", 0)
	require.NoError(t, h.Handle(ctx, rec))

	assert.Empty(t, GetBuffer(ctx).Entries())
}

func TestHandler_DelegatesToInnerHandler(t *testing.T) {
	inner := &discardHandler{}
	h := NewHandler(inner)

	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)
	require.NoError(t, h.Handle(context.Background(), rec))
	assert.True(t, inner.handled)
}

func TestHandler_WithAttrs_SetsRequestID(t *testing.T) {
	h := NewHandler(&discardHandler{})
	h2 := h.WithAttrs([]slog.Attr{slog.String("request_id", "req-via-attrs")}).(*Handler)

	ctx := NewBufferContext(context.Background())
	rec := slog.NewRecord(time.Now(), slog.LevelWarn, "warning msg", 0)
	require.NoError(t, h2.Handle(ctx, rec))

	buf := GetBuffer(ctx)
	require.Len(t, buf.Entries(), 1)
	assert.Equal(t, "warning msg", buf.Entries()[0].Message)
}

// --- Handler edge cases ---

func TestHandler_NoBuffer_InContext_DoesNotPanic(t *testing.T) {
	h := NewHandler(&discardHandler{})
	ctx := context.WithValue(context.Background(), RequestIDKey, "req-123")
	// No buffer — should not panic, just skip capture.
	rec := slog.NewRecord(time.Now(), slog.LevelError, "no buffer", 0)
	require.NoError(t, h.Handle(ctx, rec))
}

func TestHandler_CapturesCorrectLevel(t *testing.T) {
	h := NewHandler(&discardHandler{})
	ctx := context.WithValue(context.Background(), RequestIDKey, "req-lvl")
	ctx = NewBufferContext(ctx)

	levels := []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError}
	for _, lvl := range levels {
		rec := slog.NewRecord(time.Now(), lvl, "msg-"+lvl.String(), 0)
		require.NoError(t, h.Handle(ctx, rec))
	}

	buf := GetBuffer(ctx)
	require.Len(t, buf.Entries(), 4)
	assert.Equal(t, "DEBUG", buf.Entries()[0].Level)
	assert.Equal(t, "INFO", buf.Entries()[1].Level)
	assert.Equal(t, "WARN", buf.Entries()[2].Level)
	assert.Equal(t, "ERROR", buf.Entries()[3].Level)
}

func TestHandler_CapturesMultipleAttrs(t *testing.T) {
	h := NewHandler(&discardHandler{})
	ctx := context.WithValue(context.Background(), RequestIDKey, "req-attrs")
	ctx = NewBufferContext(ctx)

	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "multi-attr", 0)
	rec.AddAttrs(
		slog.String("foo", "bar"),
		slog.Int("count", 42),
	)
	require.NoError(t, h.Handle(ctx, rec))

	buf := GetBuffer(ctx)
	require.Len(t, buf.Entries(), 1)
	assert.Contains(t, buf.Entries()[0].Attrs, "foo=bar")
	assert.Contains(t, buf.Entries()[0].Attrs, "count=42")
}

func TestHandler_ExcludesRequestIDFromAttrs(t *testing.T) {
	h := NewHandler(&discardHandler{})
	ctx := context.WithValue(context.Background(), RequestIDKey, "req-exclude")
	ctx = NewBufferContext(ctx)

	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "test", 0)
	rec.AddAttrs(
		slog.String("request_id", "req-exclude"),
		slog.String("other", "value"),
	)
	require.NoError(t, h.Handle(ctx, rec))

	buf := GetBuffer(ctx)
	require.Len(t, buf.Entries(), 1)
	assert.NotContains(t, buf.Entries()[0].Attrs, "request_id")
	assert.Contains(t, buf.Entries()[0].Attrs, "other=value")
}

func TestHandler_WithGroup_PreservesRequestID(t *testing.T) {
	h := NewHandler(&discardHandler{})
	h2 := h.WithAttrs([]slog.Attr{slog.String("request_id", "req-grp")}).(*Handler)
	h3 := h2.WithGroup("mygroup").(*Handler)

	ctx := NewBufferContext(context.Background())
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "grouped", 0)
	require.NoError(t, h3.Handle(ctx, rec))

	buf := GetBuffer(ctx)
	require.Len(t, buf.Entries(), 1)
	assert.Equal(t, "grouped", buf.Entries()[0].Message)
}

func TestHandler_WithAttrs_PreservesParentAttrs(t *testing.T) {
	h := NewHandler(&discardHandler{})
	h2 := h.WithAttrs([]slog.Attr{
		slog.String("request_id", "req-parent"),
		slog.String("service", "api"),
	}).(*Handler)
	h3 := h2.WithAttrs([]slog.Attr{slog.String("version", "v2")}).(*Handler)

	ctx := NewBufferContext(context.Background())
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "test", 0)
	require.NoError(t, h3.Handle(ctx, rec))

	buf := GetBuffer(ctx)
	require.Len(t, buf.Entries(), 1)
	assert.Contains(t, buf.Entries()[0].Attrs, "service=api")
	assert.Contains(t, buf.Entries()[0].Attrs, "version=v2")
}

func TestHandler_Enabled_DelegatesToInner(t *testing.T) {
	inner := &discardHandler{}
	h := NewHandler(inner)
	assert.True(t, h.Enabled(context.Background(), slog.LevelInfo))
}
