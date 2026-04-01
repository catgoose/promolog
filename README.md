# promolog

[![Go Reference](https://pkg.go.dev/badge/github.com/catgoose/promolog.svg)](https://pkg.go.dev/github.com/catgoose/promolog)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

![promolog](https://raw.githubusercontent.com/catgoose/screenshots/main/promolog/promolog.png)

Per-request log capture with promote-on-error semantics for Go.

> past is already past -- don't debug it
>
> -- Layman Grug

Grug was almost right. But when the request fails, the past is exactly what you need. Promolog says: buffer the past, discard it when it doesn't matter, and promote it when it does.

## Why

**Without promolog:**

```go
// Every request logs everything. 99% of it is noise.
func handler(w http.ResponseWriter, r *http.Request) {
    slog.Info("parsing request body")
    slog.Info("validating input", "field", "email")
    slog.Info("querying database", "table", "users")
    // ...request succeeds. These logs are useless.
    // But when a request FAILS, you wish you had more context.
    // The error log is one line buried in thousands of success logs.
    slog.Error("database timeout", "err", err)
    // Good luck finding the 5 log lines that led to this.
}
```

**With promolog:**

```go
// Success: logs buffered in memory, then discarded. Zero noise.
// Error: entire request trace promoted to storage. Full context.
logger := slog.New(promolog.NewHandler(slog.Default().Handler()))

// In your error handler:
buf := promolog.GetBuffer(r.Context())
store.Promote(ctx, promolog.ErrorTrace{
    RequestID: requestID,
    Entries:   buf.Entries(), // every log line from this request
    Route:     "/api/users",
    Method:    "GET",
})
// Later: store.ListTraces(ctx, promolog.TraceFilter{Q: "timeout"})
```

During normal requests, log records are buffered in memory and discarded.
When a request errors, the entire buffer is promoted to a store for
later inspection. You get full request context -- every log line that led to
the failure -- without the noise of successful requests.

## Install

The core library has zero external dependencies:

```bash
go get github.com/catgoose/promolog
```

For the SQLite-backed store:

```bash
go get github.com/catgoose/promolog/sqlite
```

> The server does not remember you. The server has already forgotten you. The server has moved on.
>
> -- The Wisdom of the Uniform Interface

Unless you fail. Then the server remembers everything.

## How it works

1. Middleware attaches a `Buffer` and request ID to the context
2. A `slog.Handler` wrapper captures every log record into the buffer
3. On error, your error handler calls `Store.Promote` to persist the buffer
4. Later, query the store to see exactly what happened

```
request in --> buffer logs --> success? discard
                          \-> error?   promote to store
```

## Quick start

```go
import (
    "database/sql"
    "log/slog"

    "github.com/catgoose/promolog"
    "github.com/catgoose/promolog/sqlite"
    _ "github.com/mattn/go-sqlite3"
)

// 1. Set up the store
db, _ := sql.Open("sqlite3", "errors.db")
store := sqlite.NewStore(db)
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
    Entries:    buf.Entries(),
})
```

> Grug say: "complexity is apex predator." Student say: "how do I defeat the complexity?" Grug say: "no."
>
> -- Layman Grug

Promolog says "no" to log noise. Successful requests produce zero output. Failed requests produce everything.

> If you are building something that must evolve — while clients depend on it, while teams change, while requirements shift, while Kevin goes on PTO and comes back and the new Kevin doesn't know the old Kevin's conventions — then you need an architecture that permits change without breaking the contract.
>
> -- The Wisdom of the Uniform Interface

Promolog is that architecture for your error traces. When Kevin comes back from PTO, the full request context is waiting in the store.

## Bring your own store

The core library defines a `Storer` interface. The SQLite implementation lives
in `github.com/catgoose/promolog/sqlite`, but you can implement `Storer` with
any backend (Postgres, Redis, in-memory, etc.):

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

`Storer` is an interface -- use it to mock the store in your application tests.

## API reference

### Core (`github.com/catgoose/promolog`) -- zero dependencies

| Type | Description |
|------|-------------|
| `Storer` | Interface for trace persistence (implement or mock) |
| `Handler` | `slog.Handler` wrapper that captures records into a per-request buffer |
| `Buffer` | Thread-safe per-request log buffer |
| `ErrorTrace` | Full error trace with log entries |
| `TraceSummary` | Lightweight trace without log entries (for list views) |
| `TraceFilter` | Query parameters for `ListTraces` and `AvailableFilters` |
| `FilterOptions` | Distinct status codes and methods for filter dropdowns |
| `ErrDuplicateTrace` | Sentinel error for duplicate request IDs |

### SQLite store (`github.com/catgoose/promolog/sqlite`)

| Type | Description |
|------|-------------|
| `Store` | SQLite-backed implementation of `promolog.Storer` |
| `NewStore(db)` | Constructor -- pass a `*sql.DB` opened with a SQLite driver |

## Philosophy

> Grug's last teaching: past is already past -- don't debug it. future not here yet -- don't optimize for it. server return html -- this present moment.
>
> -- Layman Grug

Promolog amends the teaching: the past is past — unless the request failed. Then the past is exactly what you need, and promolog kept it for you.

Promolog follows the [dothog design philosophy](https://github.com/catgoose/dothog/blob/main/PHILOSOPHY.md): zero dependencies in the core, interface-driven extensibility, and the server handles state so you don't have to.

## License

MIT
