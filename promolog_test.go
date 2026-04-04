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
	assert.Equal(t, "auth", buf.Entries()[0].Attrs["component"])
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
	assert.Equal(t, "bar", buf.Entries()[0].Attrs["foo"])
	assert.Equal(t, "42", buf.Entries()[0].Attrs["count"])
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
	_, hasReqID := buf.Entries()[0].Attrs["request_id"]
	assert.False(t, hasReqID)
	assert.Equal(t, "value", buf.Entries()[0].Attrs["other"])
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
	assert.Equal(t, "api", buf.Entries()[0].Attrs["service"])
	assert.Equal(t, "v2", buf.Entries()[0].Attrs["version"])
}

func TestHandler_Enabled_DelegatesToInner(t *testing.T) {
	inner := &discardHandler{}
	h := NewHandler(inner)
	assert.True(t, h.Enabled(context.Background(), slog.LevelInfo))
}

// --- Buffer limit tests ---

func TestNewBuffer_ZeroLimit_Unlimited(t *testing.T) {
	buf := NewBuffer(0)
	for i := range 100 {
		buf.Append(Entry{Message: fmt.Sprintf("msg-%d", i)})
	}
	assert.Len(t, buf.Entries(), 100)
}

func TestNewBuffer_NegativeLimit_TreatedAsUnlimited(t *testing.T) {
	buf := NewBuffer(-5)
	for i := range 50 {
		buf.Append(Entry{Message: fmt.Sprintf("msg-%d", i)})
	}
	assert.Len(t, buf.Entries(), 50)
}

func TestBuffer_Limit_UnderLimit(t *testing.T) {
	buf := NewBuffer(10)
	for i := range 5 {
		buf.Append(Entry{Message: fmt.Sprintf("msg-%d", i)})
	}
	entries := buf.Entries()
	assert.Len(t, entries, 5)
	for i, e := range entries {
		assert.Equal(t, fmt.Sprintf("msg-%d", i), e.Message)
	}
}

func TestBuffer_Limit_ExactlyAtLimit(t *testing.T) {
	buf := NewBuffer(10)
	for i := range 10 {
		buf.Append(Entry{Message: fmt.Sprintf("msg-%d", i)})
	}
	entries := buf.Entries()
	assert.Len(t, entries, 10)
	for i, e := range entries {
		assert.Equal(t, fmt.Sprintf("msg-%d", i), e.Message)
	}
}

func TestBuffer_Limit_ExceedsLimit(t *testing.T) {
	buf := NewBuffer(10)
	for i := range 20 {
		buf.Append(Entry{Message: fmt.Sprintf("msg-%d", i)})
	}
	entries := buf.Entries()
	// 5 head + 1 synthetic + 5 tail = 11
	assert.Len(t, entries, 11)

	// Head entries: first 5
	for i := range 5 {
		assert.Equal(t, fmt.Sprintf("msg-%d", i), entries[i].Message)
	}

	// Synthetic elided entry
	assert.Equal(t, "WARN", entries[5].Level)
	assert.Contains(t, entries[5].Message, "10 log entries elided")
	assert.Contains(t, entries[5].Message, "buffer limit 10")

	// Tail entries: last 5 (msg-15 through msg-19)
	for i := range 5 {
		assert.Equal(t, fmt.Sprintf("msg-%d", 15+i), entries[6+i].Message)
	}
}

func TestBuffer_Limit_PreservesFirstAndLastEntries(t *testing.T) {
	buf := NewBuffer(6)
	for i := range 100 {
		buf.Append(Entry{Message: fmt.Sprintf("entry-%d", i)})
	}
	entries := buf.Entries()
	// 3 head + 1 synthetic + 3 tail = 7
	assert.Len(t, entries, 7)

	// First 3 entries preserved
	assert.Equal(t, "entry-0", entries[0].Message)
	assert.Equal(t, "entry-1", entries[1].Message)
	assert.Equal(t, "entry-2", entries[2].Message)

	// Synthetic entry
	assert.Equal(t, "WARN", entries[3].Level)
	assert.Contains(t, entries[3].Message, "94 log entries elided")

	// Last 3 entries preserved
	assert.Equal(t, "entry-97", entries[4].Message)
	assert.Equal(t, "entry-98", entries[5].Message)
	assert.Equal(t, "entry-99", entries[6].Message)
}

func TestBuffer_Limit_SmallLimit(t *testing.T) {
	buf := NewBuffer(2)
	for i := range 10 {
		buf.Append(Entry{Message: fmt.Sprintf("msg-%d", i)})
	}
	entries := buf.Entries()
	// 1 head + 1 synthetic + 1 tail = 3
	assert.Len(t, entries, 3)
	assert.Equal(t, "msg-0", entries[0].Message)
	assert.Contains(t, entries[1].Message, "8 log entries elided")
	assert.Equal(t, "msg-9", entries[2].Message)
}

func TestBuffer_Limit_LimitOfOne(t *testing.T) {
	// With limit=1: headSize=0, tailSize=1 — only tail is kept
	buf := NewBuffer(1)
	for i := range 5 {
		buf.Append(Entry{Message: fmt.Sprintf("msg-%d", i)})
	}
	entries := buf.Entries()
	// 0 head + 1 synthetic + 1 tail = 2
	assert.Len(t, entries, 2)
	assert.Contains(t, entries[0].Message, "4 log entries elided")
	assert.Equal(t, "msg-4", entries[1].Message)
}

func TestBuffer_Limit_ConcurrentAppend(t *testing.T) {
	buf := NewBuffer(20)
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

	entries := buf.Entries()
	// Should have 10 head + 1 elided + 10 tail = 21
	assert.Len(t, entries, 21)
	assert.Equal(t, buf.total, goroutines*perGoroutine)
}

func TestNewBufferContextWithLimit(t *testing.T) {
	ctx := NewBufferContextWithLimit(context.Background(), 10)
	buf := GetBuffer(ctx)
	require.NotNil(t, buf)
	assert.Equal(t, 10, buf.limit)
}

func TestBuffer_Limit_NoElidedWhenNotExceeded(t *testing.T) {
	buf := NewBuffer(10)
	for i := range 10 {
		buf.Append(Entry{Message: fmt.Sprintf("msg-%d", i)})
	}
	entries := buf.Entries()
	// No synthetic entry should be present
	for _, e := range entries {
		assert.NotContains(t, e.Message, "elided")
	}
}

// --- Handler WithBufferLimit tests ---

func TestWithBufferLimit(t *testing.T) {
	h := NewHandler(&discardHandler{}, WithBufferLimit(50))
	assert.Equal(t, 50, h.BufferLimit())
}

func TestWithBufferLimit_PropagatedThroughWithAttrs(t *testing.T) {
	h := NewHandler(&discardHandler{}, WithBufferLimit(50))
	h2 := h.WithAttrs([]slog.Attr{slog.String("key", "val")}).(*Handler)
	assert.Equal(t, 50, h2.BufferLimit())
}

func TestWithBufferLimit_PropagatedThroughWithGroup(t *testing.T) {
	h := NewHandler(&discardHandler{}, WithBufferLimit(50))
	h2 := h.WithGroup("grp").(*Handler)
	assert.Equal(t, 50, h2.BufferLimit())
}
