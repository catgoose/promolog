package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/catgoose/promolog"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
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

func sampleTrace(requestID string, statusCode int, method string) promolog.Trace {
	return promolog.Trace{
		RequestID:  requestID,
		ErrorChain: "something went wrong",
		StatusCode: statusCode,
		Route:      "/api/test",
		Method:     method,
		UserAgent:  "TestAgent/1.0",
		RemoteIP:   "127.0.0.1",
		UserID:     "user-42",
		Entries: []promolog.Entry{
			{Time: time.Now(), Level: "ERROR", Message: "test error", Attrs: map[string]string{"key": "val"}},
		},
	}
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
	assert.True(t, errors.Is(err, promolog.ErrDuplicateTrace))
}

func TestPromote_DuplicateDoesNotFireCallback(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	callCount := 0
	store.SetOnPromote(func(_ promolog.TraceSummary) { callCount++ })

	require.NoError(t, store.Promote(ctx, sampleTrace("req-dup2", 500, "GET")))
	_ = store.Promote(ctx, sampleTrace("req-dup2", 502, "POST"))
	assert.Equal(t, 1, callCount)
}

func TestPromote_FiresOnPromoteCallback(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	var received promolog.TraceSummary
	called := false
	store.SetOnPromote(func(ts promolog.TraceSummary) {
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

	rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{Page: 1, PerPage: 2})
	require.NoError(t, err)
	assert.Equal(t, 5, total)
	assert.Len(t, rows, 2)
}

func TestListTraces_DefaultsPageAndPerPage(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-default", 500, "GET")))

	rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{})
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

	rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{Q: "special", Page: 1, PerPage: 10})
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

	rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{Q: "100%", Page: 1, PerPage: 10})
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

	rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{Status: "4xx", Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, rows, 2)

	rows, total, err = store.ListTraces(ctx, promolog.TraceFilter{Status: "5xx", Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, rows, 2)
}

func TestListTraces_MethodFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-get", 500, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-post", 500, "POST")))

	rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{Method: "POST", Page: 1, PerPage: 10})
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

	opts, err := store.AvailableFilters(ctx, promolog.TraceFilter{})
	require.NoError(t, err)
	assert.Equal(t, []int{400, 500}, opts.StatusCodes)
	assert.Equal(t, []string{"GET", "POST"}, opts.Methods)
}

func TestAvailableFilters_ExcludesOwnDimension(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-1", 400, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-2", 500, "POST")))

	opts, err := store.AvailableFilters(ctx, promolog.TraceFilter{Method: "GET"})
	require.NoError(t, err)
	assert.Equal(t, []int{400}, opts.StatusCodes)
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
	time.Sleep(100 * time.Millisecond)
}

// --- InitSchema idempotency ---

func TestInitSchema_Idempotent(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)
	require.NoError(t, store.InitSchema())
	require.NoError(t, store.InitSchema())
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
	trace.Entries = []promolog.Entry{
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
	require.NoError(t, store.Promote(context.Background(), sampleTrace("req-nil-cb", 500, "GET")))
}

func TestPromoteAt_CallbackReceivesCorrectTimestamp(t *testing.T) {
	store := newTestStore(t)
	ts := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	var received promolog.TraceSummary
	store.SetOnPromote(func(s promolog.TraceSummary) { received = s })

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

	rows, _, err := store.ListTraces(ctx, promolog.TraceFilter{
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

	rows, _, err := store.ListTraces(ctx, promolog.TraceFilter{
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

	rows, _, err := store.ListTraces(ctx, promolog.TraceFilter{Page: 1, PerPage: 10})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "req-new", rows[0].RequestID)
	assert.Equal(t, "req-old", rows[1].RequestID)
}

func TestListTraces_InvalidSortFallsBackToCreatedAt(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	ts1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ts2 := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.PromoteAt(ctx, sampleTrace("req-old", 500, "GET"), ts1))
	require.NoError(t, store.PromoteAt(ctx, sampleTrace("req-new", 500, "GET"), ts2))

	rows, _, err := store.ListTraces(ctx, promolog.TraceFilter{
		Sort: "Bogus", Dir: "asc", Page: 1, PerPage: 10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "req-old", rows[0].RequestID)
	assert.Equal(t, "req-new", rows[1].RequestID)
}

func TestListTraces_MultiWordSearch(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t1 := sampleTrace("req-1", 500, "GET")
	t1.Route = "/api/users"
	t1.ErrorChain = "connection refused"
	require.NoError(t, store.Promote(ctx, t1))

	t2 := sampleTrace("req-2", 404, "GET")
	t2.Route = "/api/orders"
	t2.ErrorChain = "not found"
	require.NoError(t, store.Promote(ctx, t2))

	rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{Q: "users connection", Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-1", rows[0].RequestID)
}

func TestListTraces_ExactStatusCode(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-502", 502, "GET")))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-503", 503, "GET")))

	rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{Status: "502", Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-502", rows[0].RequestID)
}

// --- Tag tests ---

func TestPromote_WithTags_Roundtrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	trace := sampleTrace("req-tags", 500, "GET")
	trace.Tags = map[string]string{"feature": "checkout", "tenant": "acme"}
	require.NoError(t, store.Promote(ctx, trace))

	got, err := store.Get(ctx, "req-tags")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "checkout", got.Tags["feature"])
	assert.Equal(t, "acme", got.Tags["tenant"])
}

func TestPromote_WithoutTags_ReturnsNilTags(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-notags", 500, "GET")))

	got, err := store.Get(ctx, "req-notags")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.Tags)
}

func TestListTraces_TagFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	t1 := sampleTrace("req-t1", 500, "GET")
	t1.Tags = map[string]string{"feature": "checkout"}
	t2 := sampleTrace("req-t2", 500, "GET")
	t2.Tags = map[string]string{"feature": "login"}
	t3 := sampleTrace("req-t3", 500, "GET")
	// no tags

	require.NoError(t, store.Promote(ctx, t1))
	require.NoError(t, store.Promote(ctx, t2))
	require.NoError(t, store.Promote(ctx, t3))

	rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{
		Tags: map[string]string{"feature": "checkout"},
		Page: 1, PerPage: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-t1", rows[0].RequestID)
	assert.Equal(t, "checkout", rows[0].Tags["feature"])
}

func TestListTraces_TagFilter_MultipleTagsAND(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	t1 := sampleTrace("req-m1", 500, "GET")
	t1.Tags = map[string]string{"feature": "checkout", "tenant": "acme"}
	t2 := sampleTrace("req-m2", 500, "GET")
	t2.Tags = map[string]string{"feature": "checkout", "tenant": "other"}

	require.NoError(t, store.Promote(ctx, t1))
	require.NoError(t, store.Promote(ctx, t2))

	rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{
		Tags: map[string]string{"feature": "checkout", "tenant": "acme"},
		Page: 1, PerPage: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "req-m1", rows[0].RequestID)
}

func TestAvailableFilters_ReturnsTagKeys(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	t1 := sampleTrace("req-tk1", 500, "GET")
	t1.Tags = map[string]string{"feature": "checkout", "tenant": "acme"}
	t2 := sampleTrace("req-tk2", 500, "GET")
	t2.Tags = map[string]string{"env": "prod"}

	require.NoError(t, store.Promote(ctx, t1))
	require.NoError(t, store.Promote(ctx, t2))

	opts, err := store.AvailableFilters(ctx, promolog.TraceFilter{})
	require.NoError(t, err)
	assert.Equal(t, []string{"env", "feature", "tenant"}, opts.TagKeys)
}

func TestPromote_CallbackIncludesTags(t *testing.T) {
	store := newTestStore(t)
	var received promolog.TraceSummary
	store.SetOnPromote(func(ts promolog.TraceSummary) { received = ts })

	trace := sampleTrace("req-cb-tags", 500, "GET")
	trace.Tags = map[string]string{"feature": "checkout"}
	require.NoError(t, store.Promote(context.Background(), trace))

	assert.Equal(t, "checkout", received.Tags["feature"])
}

// --- Storer interface compliance ---

func TestStore_ImplementsStorer(t *testing.T) {
	var _ promolog.Storer = (*Store)(nil)
}
