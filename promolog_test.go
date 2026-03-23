package promolog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db := openTestDB(t)
	store := NewStore(db)
	require.NoError(t, store.InitSchema())
	return store
}

func sampleTrace(requestID string, statusCode int, method string) ErrorTrace {
	return ErrorTrace{
		RequestID:  requestID,
		ErrorChain: "something went wrong",
		StatusCode: statusCode,
		Route:      "/api/test",
		Method:     method,
		UserAgent:  "TestAgent/1.0",
		RemoteIP:   "127.0.0.1",
		UserID:     "user-42",
		Entries: []Entry{
			{Time: time.Now(), Level: "ERROR", Message: "test error", Attrs: "key=val"},
		},
	}
}

// --- Buffer context tests ---

func TestNewBufferContext_GetBuffer_Roundtrip(t *testing.T) {
	ctx := NewBufferContext(context.Background())
	buf := GetBuffer(ctx)
	require.NotNil(t, buf)
	assert.Empty(t, buf.Entries)
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

	snap := buf.Snapshot()
	assert.Len(t, snap, 2)
	// Snapshot is a copy — mutating it doesn't affect the buffer.
	snap[0].Message = "mutated"
	assert.Equal(t, "hello", buf.Entries[0].Message)
}

// --- Store.Promote / Get tests ---

func TestPromote_AndGet_Roundtrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	trace := sampleTrace("req-1", 500, "GET")
	require.NoError(t, store.Promote(ctx, trace))

	got, err := store.Get(ctx, "req-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "req-1", got.RequestID)
	assert.Equal(t, 500, got.StatusCode)
	assert.Equal(t, "GET", got.Method)
	assert.Equal(t, "/api/test", got.Route)
	assert.Equal(t, "something went wrong", got.ErrorChain)
	assert.Equal(t, "TestAgent/1.0", got.UserAgent)
	assert.Equal(t, "127.0.0.1", got.RemoteIP)
	assert.Equal(t, "user-42", got.UserID)
	require.Len(t, got.Entries, 1)
	assert.Equal(t, "test error", got.Entries[0].Message)
}

func TestPromote_DuplicateRequestID_ReturnsError(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-dup", 500, "GET")))

	err := store.Promote(ctx, sampleTrace("req-dup", 502, "POST"))
	assert.True(t, errors.Is(err, ErrDuplicateTrace))
}

func TestPromote_DuplicateDoesNotFireCallback(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	callCount := 0
	store.SetOnPromote(func(_ TraceSummary) { callCount++ })

	require.NoError(t, store.Promote(ctx, sampleTrace("req-dup2", 500, "GET")))
	_ = store.Promote(ctx, sampleTrace("req-dup2", 502, "POST"))
	assert.Equal(t, 1, callCount)
}

func TestPromote_FiresOnPromoteCallback(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	var received TraceSummary
	called := false
	store.SetOnPromote(func(ts TraceSummary) {
		called = true
		received = ts
	})

	require.NoError(t, store.Promote(ctx, sampleTrace("req-cb", 503, "POST")))
	require.True(t, called)
	assert.Equal(t, "req-cb", received.RequestID)
	assert.Equal(t, 503, received.StatusCode)
}

func TestGet_UnknownRequestID_ReturnsNil(t *testing.T) {
	store := newTestStore(t)
	got, err := store.Get(context.Background(), "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// --- ListTraces tests ---

func TestListTraces_BasicPagination(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for i := range 5 {
		require.NoError(t, store.Promote(ctx, sampleTrace("req-"+string(rune('a'+i)), 500, "GET")))
	}

	rows, total, err := store.ListTraces(ctx, TraceFilter{Page: 1, PerPage: 2})
	require.NoError(t, err)
	assert.Equal(t, 5, total)
	assert.Len(t, rows, 2)
}

func TestListTraces_DefaultsPageAndPerPage(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-default", 500, "GET")))

	// Page=0 and PerPage=0 should not panic and should return results.
	rows, total, err := store.ListTraces(ctx, TraceFilter{})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, rows, 1)
}

func TestListTraces_SearchFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-alpha", 500, "GET")))
	t2 := sampleTrace("req-beta", 404, "POST")
	t2.Route = "/api/special"
	require.NoError(t, store.Promote(ctx, t2))

	rows, total, err := store.ListTraces(ctx, TraceFilter{Q: "special", Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-beta", rows[0].RequestID)
}

func TestListTraces_SearchEscapesLikeWildcards(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	tr := sampleTrace("req-wild", 500, "GET")
	tr.Route = "/api/100%_done"
	require.NoError(t, store.Promote(ctx, tr))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-other", 500, "GET")))

	// Searching for literal "%" should only match the route containing it.
	rows, total, err := store.ListTraces(ctx, TraceFilter{Q: "100%", Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-wild", rows[0].RequestID)
}

func TestListTraces_StatusFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-400", 400, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-404", 404, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-500", 500, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-502", 502, "GET")))

	rows, total, err := store.ListTraces(ctx, TraceFilter{Status: "4xx", Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, rows, 2)

	rows, total, err = store.ListTraces(ctx, TraceFilter{Status: "5xx", Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, rows, 2)
}

func TestListTraces_MethodFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-get", 500, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-post", 500, "POST")))

	rows, total, err := store.ListTraces(ctx, TraceFilter{Method: "POST", Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-post", rows[0].RequestID)
}

func TestDeleteTrace(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-del", 500, "GET")))
	got, err := store.Get(ctx, "req-del")
	require.NoError(t, err)
	require.NotNil(t, got)

	require.NoError(t, store.DeleteTrace(ctx, "req-del"))
	got, err = store.Get(ctx, "req-del")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestPromoteAt_StoresCustomTimestamp(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	ts := time.Date(2024, 6, 15, 12, 30, 0, 0, time.UTC)
	require.NoError(t, store.PromoteAt(ctx, sampleTrace("req-ts", 500, "GET"), ts))

	got, err := store.Get(ctx, "req-ts")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 2024, got.CreatedAt.Year())
	assert.Equal(t, time.June, got.CreatedAt.Month())
	assert.Equal(t, 15, got.CreatedAt.Day())
}

// --- AvailableFilters tests ---

func TestAvailableFilters_ReturnsDistinctValues(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-1", 400, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-2", 500, "POST")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-3", 500, "GET")))

	opts, err := store.AvailableFilters(ctx, TraceFilter{})
	require.NoError(t, err)
	assert.Equal(t, []int{400, 500}, opts.StatusCodes)
	assert.Equal(t, []string{"GET", "POST"}, opts.Methods)
}

func TestAvailableFilters_ExcludesOwnDimension(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-1", 400, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-2", 500, "POST")))

	// Filtering by method=GET: status codes exclude the method filter (shows all),
	// but methods exclude the status filter (shows only methods matching method=GET).
	opts, err := store.AvailableFilters(ctx, TraceFilter{Method: "GET"})
	require.NoError(t, err)
	// Status codes are not filtered by method (excluded), so only the one matching GET.
	assert.Equal(t, []int{400}, opts.StatusCodes)
	// Methods exclude the method filter itself, so both show up.
	assert.Equal(t, []string{"GET", "POST"}, opts.Methods)
}

// --- StartCleanup tests ---

func TestStartCleanup_DeletesExpiredTraces(t *testing.T) {
	store := newTestStore(t)
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	defer cleanupCancel()
	ctx := context.Background()

	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, store.PromoteAt(ctx, sampleTrace("req-old", 500, "GET"), old))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-new", 500, "GET")))

	// TTL=24h means the 48h-old trace should be cleaned up.
	store.StartCleanup(cleanupCtx, 24*time.Hour, 50*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	got, err := store.Get(ctx, "req-old")
	require.NoError(t, err)
	assert.Nil(t, got, "expired trace should be deleted")

	got, err = store.Get(ctx, "req-new")
	require.NoError(t, err)
	assert.NotNil(t, got, "fresh trace should survive")
}

func TestStartCleanup_StopsOnContextCancel(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())

	store.StartCleanup(ctx, time.Hour, 50*time.Millisecond)
	cancel()
	// Just verifying it doesn't hang or panic after cancel.
	time.Sleep(100 * time.Millisecond)
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
	require.Len(t, buf.Entries, 1)
	assert.Equal(t, "something broke", buf.Entries[0].Message)
	assert.Contains(t, buf.Entries[0].Attrs, "component=auth")
}

func TestHandler_DoesNotCapture_WhenNoRequestID(t *testing.T) {
	h := NewHandler(&discardHandler{})
	ctx := NewBufferContext(context.Background())

	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "no request id", 0)
	require.NoError(t, h.Handle(ctx, rec))

	assert.Empty(t, GetBuffer(ctx).Entries)
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
	require.Len(t, buf.Entries, 1)
	assert.Equal(t, "warning msg", buf.Entries[0].Message)
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

// --- InitSchema idempotency ---

func TestInitSchema_Idempotent(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)
	require.NoError(t, store.InitSchema())
	require.NoError(t, store.InitSchema()) // second call should not error
}

// --- Promote edge cases ---

func TestPromote_EmptyEntries(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	trace := sampleTrace("req-empty", 500, "GET")
	trace.Entries = nil
	require.NoError(t, store.Promote(ctx, trace))

	got, err := store.Get(ctx, "req-empty")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, got.Entries)
}

func TestPromote_MultipleEntries_PreservesOrder(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	trace := sampleTrace("req-multi", 500, "GET")
	trace.Entries = []Entry{
		{Time: time.Now(), Level: "INFO", Message: "first"},
		{Time: time.Now(), Level: "WARN", Message: "second"},
		{Time: time.Now(), Level: "ERROR", Message: "third"},
	}
	require.NoError(t, store.Promote(ctx, trace))

	got, err := store.Get(ctx, "req-multi")
	require.NoError(t, err)
	require.Len(t, got.Entries, 3)
	assert.Equal(t, "first", got.Entries[0].Message)
	assert.Equal(t, "second", got.Entries[1].Message)
	assert.Equal(t, "third", got.Entries[2].Message)
}

func TestPromote_NilOnPromote_DoesNotPanic(t *testing.T) {
	store := newTestStore(t)
	// onPromote is nil by default — should not panic.
	require.NoError(t, store.Promote(context.Background(), sampleTrace("req-nil-cb", 500, "GET")))
}

func TestPromoteAt_CallbackReceivesCorrectTimestamp(t *testing.T) {
	store := newTestStore(t)
	ts := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	var received TraceSummary
	store.SetOnPromote(func(s TraceSummary) { received = s })

	require.NoError(t, store.PromoteAt(context.Background(), sampleTrace("req-ts-cb", 500, "GET"), ts))
	assert.Equal(t, ts, received.CreatedAt)
}

// --- ListTraces sorting ---

func TestListTraces_SortByStatusCode(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-500", 500, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-400", 400, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-502", 502, "GET")))

	rows, _, err := store.ListTraces(ctx, TraceFilter{
		Sort: "StatusCode", Dir: "asc", Page: 1, PerPage: 10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, 400, rows[0].StatusCode)
	assert.Equal(t, 500, rows[1].StatusCode)
	assert.Equal(t, 502, rows[2].StatusCode)
}

func TestListTraces_SortByRoute(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t1 := sampleTrace("req-z", 500, "GET")
	t1.Route = "/z-route"
	t2 := sampleTrace("req-a", 500, "GET")
	t2.Route = "/a-route"
	require.NoError(t, store.Promote(ctx, t1))
	require.NoError(t, store.Promote(ctx, t2))

	rows, _, err := store.ListTraces(ctx, TraceFilter{
		Sort: "Route", Dir: "asc", Page: 1, PerPage: 10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "/a-route", rows[0].Route)
	assert.Equal(t, "/z-route", rows[1].Route)
}

func TestListTraces_SortDescByDefault(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	ts1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ts2 := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.PromoteAt(ctx, sampleTrace("req-old", 500, "GET"), ts1))
	require.NoError(t, store.PromoteAt(ctx, sampleTrace("req-new", 500, "GET"), ts2))

	rows, _, err := store.ListTraces(ctx, TraceFilter{Page: 1, PerPage: 10})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "req-new", rows[0].RequestID) // newer first (DESC)
	assert.Equal(t, "req-old", rows[1].RequestID)
}

func TestListTraces_InvalidSortFallsBackToCreatedAt(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	ts1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ts2 := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.PromoteAt(ctx, sampleTrace("req-old", 500, "GET"), ts1))
	require.NoError(t, store.PromoteAt(ctx, sampleTrace("req-new", 500, "GET"), ts2))

	rows, _, err := store.ListTraces(ctx, TraceFilter{
		Sort: "Bogus", Dir: "asc", Page: 1, PerPage: 10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "req-old", rows[0].RequestID) // ASC by created_at
}

// --- ListTraces pagination ---

func TestListTraces_Page2(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for i := range 5 {
		ts := time.Date(2025, 1, 1+i, 0, 0, 0, 0, time.UTC)
		require.NoError(t, store.PromoteAt(ctx, sampleTrace(fmt.Sprintf("req-%d", i), 500, "GET"), ts))
	}

	rows, total, err := store.ListTraces(ctx, TraceFilter{
		Page: 2, PerPage: 2, Dir: "asc",
	})
	require.NoError(t, err)
	assert.Equal(t, 5, total)
	assert.Len(t, rows, 2)
	assert.Equal(t, "req-2", rows[0].RequestID)
	assert.Equal(t, "req-3", rows[1].RequestID)
}

func TestListTraces_LastPagePartialResults(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for i := range 3 {
		require.NoError(t, store.Promote(ctx, sampleTrace(fmt.Sprintf("req-%d", i), 500, "GET")))
	}

	rows, total, err := store.ListTraces(ctx, TraceFilter{Page: 2, PerPage: 2})
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Len(t, rows, 1) // only 1 left on page 2
}

func TestListTraces_BeyondLastPage_ReturnsEmpty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-1", 500, "GET")))

	rows, total, err := store.ListTraces(ctx, TraceFilter{Page: 99, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Empty(t, rows)
}

func TestListTraces_EmptyStore(t *testing.T) {
	store := newTestStore(t)
	rows, total, err := store.ListTraces(context.Background(), TraceFilter{Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, rows)
}

// --- ListTraces combined filters ---

func TestListTraces_CombinedSearchAndStatus(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t1 := sampleTrace("req-1", 500, "GET")
	t1.Route = "/api/users"
	t2 := sampleTrace("req-2", 400, "GET")
	t2.Route = "/api/users"
	t3 := sampleTrace("req-3", 500, "POST")
	t3.Route = "/api/orders"
	require.NoError(t, store.Promote(ctx, t1))
	require.NoError(t, store.Promote(ctx, t2))
	require.NoError(t, store.Promote(ctx, t3))

	rows, total, err := store.ListTraces(ctx, TraceFilter{
		Q: "users", Status: "5xx", Page: 1, PerPage: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-1", rows[0].RequestID)
}

func TestListTraces_CombinedAllFilters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t1 := sampleTrace("req-1", 500, "POST")
	t1.Route = "/api/users"
	t2 := sampleTrace("req-2", 500, "GET")
	t2.Route = "/api/users"
	t3 := sampleTrace("req-3", 500, "POST")
	t3.Route = "/api/orders"
	require.NoError(t, store.Promote(ctx, t1))
	require.NoError(t, store.Promote(ctx, t2))
	require.NoError(t, store.Promote(ctx, t3))

	rows, total, err := store.ListTraces(ctx, TraceFilter{
		Q: "users", Status: "5xx", Method: "POST", Page: 1, PerPage: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-1", rows[0].RequestID)
}

// --- ListTraces multi-word search ---

func TestListTraces_MultiWordSearch(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t1 := sampleTrace("req-1", 500, "GET")
	t1.Route = "/api/users"
	t1.ErrorChain = "connection refused"
	t2 := sampleTrace("req-2", 500, "GET")
	t2.Route = "/api/users"
	t2.ErrorChain = "timeout"
	t3 := sampleTrace("req-3", 500, "GET")
	t3.Route = "/api/orders"
	t3.ErrorChain = "connection refused"
	require.NoError(t, store.Promote(ctx, t1))
	require.NoError(t, store.Promote(ctx, t2))
	require.NoError(t, store.Promote(ctx, t3))

	// Both terms must match (AND): "users" AND "refused"
	rows, total, err := store.ListTraces(ctx, TraceFilter{
		Q: "users refused", Page: 1, PerPage: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-1", rows[0].RequestID)
}

// --- ListTraces status filter edge cases ---

func TestListTraces_ExactStatusCode(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-400", 400, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-404", 404, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-500", 500, "GET")))

	rows, total, err := store.ListTraces(ctx, TraceFilter{
		Status: "404", Page: 1, PerPage: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-404", rows[0].RequestID)
}

func TestListTraces_InvalidStatusFilterIgnored(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-1", 500, "GET")))

	// "bogus" can't be parsed as int, should be ignored (return all).
	rows, total, err := store.ListTraces(ctx, TraceFilter{
		Status: "bogus", Page: 1, PerPage: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, rows, 1)
}

// --- ListTraces search across different columns ---

func TestListTraces_SearchMatchesRequestID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("unique-req-xyz", 500, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-other", 500, "GET")))

	rows, total, err := store.ListTraces(ctx, TraceFilter{Q: "unique-req-xyz", Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "unique-req-xyz", rows[0].RequestID)
}

func TestListTraces_SearchMatchesUserID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t1 := sampleTrace("req-1", 500, "GET")
	t1.UserID = "admin-special"
	require.NoError(t, store.Promote(ctx, t1))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-2", 500, "GET")))

	rows, _, err := store.ListTraces(ctx, TraceFilter{Q: "admin-special", Page: 1, PerPage: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-1", rows[0].RequestID)
}

func TestListTraces_SearchMatchesErrorChain(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t1 := sampleTrace("req-1", 500, "GET")
	t1.ErrorChain = "unique-sentinel-error"
	require.NoError(t, store.Promote(ctx, t1))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-2", 500, "GET")))

	rows, _, err := store.ListTraces(ctx, TraceFilter{Q: "unique-sentinel", Page: 1, PerPage: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-1", rows[0].RequestID)
}

func TestListTraces_SearchMatchesRemoteIP(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t1 := sampleTrace("req-1", 500, "GET")
	t1.RemoteIP = "10.99.88.77"
	require.NoError(t, store.Promote(ctx, t1))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-2", 500, "GET")))

	rows, _, err := store.ListTraces(ctx, TraceFilter{Q: "10.99.88", Page: 1, PerPage: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-1", rows[0].RequestID)
}

// --- LIKE escape edge cases ---

func TestListTraces_SearchWithUnderscore(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t1 := sampleTrace("req-1", 500, "GET")
	t1.Route = "/api/foo_bar"
	t2 := sampleTrace("req-2", 500, "GET")
	t2.Route = "/api/fooXbar" // _ would match X without escaping
	require.NoError(t, store.Promote(ctx, t1))
	require.NoError(t, store.Promote(ctx, t2))

	rows, total, err := store.ListTraces(ctx, TraceFilter{Q: "foo_bar", Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-1", rows[0].RequestID)
}

// --- DeleteTrace edge cases ---

func TestDeleteTrace_NonexistentID_NoError(t *testing.T) {
	store := newTestStore(t)
	err := store.DeleteTrace(context.Background(), "does-not-exist")
	assert.NoError(t, err)
}

// --- AvailableFilters edge cases ---

func TestAvailableFilters_EmptyStore(t *testing.T) {
	store := newTestStore(t)
	opts, err := store.AvailableFilters(context.Background(), TraceFilter{})
	require.NoError(t, err)
	assert.Nil(t, opts.StatusCodes)
	assert.Nil(t, opts.Methods)
}

func TestAvailableFilters_WithSearchFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t1 := sampleTrace("req-1", 400, "GET")
	t1.Route = "/api/users"
	t2 := sampleTrace("req-2", 500, "POST")
	t2.Route = "/api/orders"
	require.NoError(t, store.Promote(ctx, t1))
	require.NoError(t, store.Promote(ctx, t2))

	opts, err := store.AvailableFilters(ctx, TraceFilter{Q: "users"})
	require.NoError(t, err)
	assert.Equal(t, []int{400}, opts.StatusCodes)
	assert.Equal(t, []string{"GET"}, opts.Methods)
}

func TestAvailableFilters_WithStatusFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-1", 400, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-2", 500, "POST")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-3", 500, "DELETE")))

	opts, err := store.AvailableFilters(ctx, TraceFilter{Status: "5xx"})
	require.NoError(t, err)
	// Status codes exclude the status filter itself, so all show up.
	assert.Equal(t, []int{400, 500}, opts.StatusCodes)
	// Methods are filtered by status=5xx.
	assert.Equal(t, []string{"DELETE", "POST"}, opts.Methods)
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
	require.Len(t, buf.Entries, 4)
	assert.Equal(t, "DEBUG", buf.Entries[0].Level)
	assert.Equal(t, "INFO", buf.Entries[1].Level)
	assert.Equal(t, "WARN", buf.Entries[2].Level)
	assert.Equal(t, "ERROR", buf.Entries[3].Level)
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
	require.Len(t, buf.Entries, 1)
	assert.Contains(t, buf.Entries[0].Attrs, "foo=bar")
	assert.Contains(t, buf.Entries[0].Attrs, "count=42")
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
	require.Len(t, buf.Entries, 1)
	assert.NotContains(t, buf.Entries[0].Attrs, "request_id")
	assert.Contains(t, buf.Entries[0].Attrs, "other=value")
}

func TestHandler_WithGroup_PreservesRequestID(t *testing.T) {
	h := NewHandler(&discardHandler{})
	h2 := h.WithAttrs([]slog.Attr{slog.String("request_id", "req-grp")}).(*Handler)
	h3 := h2.WithGroup("mygroup").(*Handler)

	ctx := NewBufferContext(context.Background())
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "grouped", 0)
	require.NoError(t, h3.Handle(ctx, rec))

	buf := GetBuffer(ctx)
	require.Len(t, buf.Entries, 1)
	assert.Equal(t, "grouped", buf.Entries[0].Message)
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
	require.Len(t, buf.Entries, 1)
	assert.Contains(t, buf.Entries[0].Attrs, "service=api")
	assert.Contains(t, buf.Entries[0].Attrs, "version=v2")
}

func TestHandler_Enabled_DelegatesToInner(t *testing.T) {
	inner := &discardHandler{}
	h := NewHandler(inner)
	assert.True(t, h.Enabled(context.Background(), slog.LevelInfo))
}

// --- escapeLike unit tests ---

func TestEscapeLike(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"100%", `100\%`},
		{"foo_bar", `foo\_bar`},
		{`back\slash`, `back\\slash`},
		{`%_\`, `\%\_\\`},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, escapeLike(tt.input))
		})
	}
}

// --- Storer interface test ---

func TestStore_ImplementsStorer(t *testing.T) {
	var _ Storer = (*Store)(nil)
}
