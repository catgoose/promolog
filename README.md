# promolog

Per-request log capture with promote-on-error semantics for Go.

During normal requests, log records are buffered in memory and discarded.
When a request errors, the entire buffer is promoted to a SQLite store for
later inspection. You get full request context -- every log line that led to
the failure -- without the noise of successful requests.

## Install

```bash
go get github.com/catgoose/promolog
```

## How it works

1. Middleware attaches a `Buffer` and request ID to the context
2. A `slog.Handler` wrapper captures every log record into the buffer
3. On error, your error handler calls `Store.Promote` to persist the buffer
4. Later, query the store to see exactly what happened

```
request in --> buffer logs --> success? discard
                          \-> error?   promote to SQLite
```

## Quick start

```go
import (
    "database/sql"
    "log/slog"

    "github.com/catgoose/promolog"
    _ "github.com/mattn/go-sqlite3"
)

// 1. Set up the store
db, _ := sql.Open("sqlite3", "errors.db")
store := promolog.NewStore(db)
store.InitSchema()
store.StartCleanup(ctx, 90*24*time.Hour, time.Hour)

// 2. Wrap your slog handler
logger := slog.New(promolog.NewHandler(slog.Default().Handler()))

// 3. In your middleware, attach a buffer and request ID
ctx = context.WithValue(ctx, promolog.RequestIDKey, requestID)
ctx = promolog.NewBufferContext(ctx)

// 4. On error, promote the buffer
buf := promolog.GetBuffer(ctx)
err := store.Promote(ctx, promolog.ErrorTrace{
    RequestID:  requestID,
    ErrorChain: err.Error(),
    StatusCode: 500,
    Route:      "/api/users",
    Method:     "GET",
    UserAgent:  r.UserAgent(),
    RemoteIP:   r.RemoteAddr,
    UserID:     userID,
    Entries:    buf.Snapshot(),
})
```

## Querying traces

```go
// Get a single trace by request ID
trace, err := store.Get(ctx, "req-abc-123")

// List traces with filtering, search, sorting, and pagination
rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{
    Q:       "connection",     // search across route, error, request ID, user, IP
    Status:  "5xx",            // "4xx", "5xx", or exact code like "502"
    Method:  "POST",           // HTTP method filter
    Sort:    "StatusCode",     // CreatedAt (default), StatusCode, Route, Method
    Dir:     "asc",            // asc or desc (default)
    Page:    1,                // defaults to 1
    PerPage: 25,               // defaults to 25
})

// Get distinct values for building filter dropdowns
opts, _ := store.AvailableFilters(ctx, promolog.TraceFilter{})
// opts.StatusCodes: []int{400, 500, 502}
// opts.Methods:     []string{"GET", "POST"}

// Delete a trace
store.DeleteTrace(ctx, "req-abc-123")
```

## Duplicate handling

`Promote` returns `promolog.ErrDuplicateTrace` if a trace with the same
request ID already exists. The `SetOnPromote` callback only fires on
successful inserts.

```go
err := store.Promote(ctx, trace)
if errors.Is(err, promolog.ErrDuplicateTrace) {
    // already recorded
}
```

## Promote callback

Register a callback for real-time notifications (e.g. SSE, webhooks):

```go
store.SetOnPromote(func(ts promolog.TraceSummary) {
    log.Printf("new error: %s %d %s", ts.RequestID, ts.StatusCode, ts.Route)
})
```

## Testing

`Storer` is an interface satisfied by `*Store`, enforced at compile time:

```go
type Storer interface {
    InitSchema() error
    SetOnPromote(fn func(TraceSummary))
    Promote(ctx context.Context, trace ErrorTrace) error
    PromoteAt(ctx context.Context, trace ErrorTrace, createdAt time.Time) error
    Get(ctx context.Context, requestID string) (*ErrorTrace, error)
    ListTraces(ctx context.Context, f TraceFilter) ([]TraceSummary, int, error)
    AvailableFilters(ctx context.Context, f TraceFilter) (FilterOptions, error)
    DeleteTrace(ctx context.Context, requestID string) error
    StartCleanup(ctx context.Context, ttl time.Duration, interval time.Duration)
}
```

Use it to mock the store in your application tests.

## API reference

| Type | Description |
|------|-------------|
| `Store` | SQLite-backed trace storage |
| `Storer` | Interface for mocking |
| `Handler` | `slog.Handler` wrapper that captures records into a per-request buffer |
| `Buffer` | Thread-safe per-request log buffer |
| `ErrorTrace` | Full error trace with log entries |
| `TraceSummary` | Lightweight trace without log entries (for list views) |
| `TraceFilter` | Query parameters for `ListTraces` and `AvailableFilters` |
| `FilterOptions` | Distinct status codes and methods for filter dropdowns |
| `ErrDuplicateTrace` | Sentinel error for duplicate request IDs |

## License

MIT
