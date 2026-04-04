# promolog

<!--toc:start-->

- [promolog](#promolog)
  - [Why](#why)
  - [Install](#install)
  - [How it works](#how-it-works)
  - [Quick start](#quick-start)
  - [Promotion policies](#promotion-policies)
    - [Built-in policies](#built-in-policies)
    - [Auto-promote middleware](#auto-promote-middleware)
    - [Runtime filter rules](#runtime-filter-rules)
    - [Manual promotion](#manual-promotion)
  - [Middleware stack](#middleware-stack)
    - [Correlation](#correlation)
    - [Body capture](#body-capture)
    - [Buffer limits](#buffer-limits)
    - [Putting it together](#putting-it-together)
  - [Distributed tracing](#distributed-tracing)
  - [Trace tags](#trace-tags)
  - [Querying traces](#querying-traces)
    - [Aggregation](#aggregation)
    - [Available filters](#available-filters)
  - [Retention policies](#retention-policies)
  - [Export adapters](#export-adapters)
  - [Bring your own store](#bring-your-own-store)
  - [Duplicate handling](#duplicate-handling)
  - [Testing](#testing)
  - [API reference](#api-reference)
    - [Core (`github.com/catgoose/promolog`) -- zero dependencies](#core-githubcomcatgoosepromolog----zero-dependencies)
    - [SQLite store (`github.com/catgoose/promolog/sqlite`)](#sqlite-store-githubcomcatgoosepromologsqlite)
    - [Export packages](#export-packages)
  - [Philosophy](#philosophy)
  - [Architecture](#architecture)
  - [License](#license)
  <!--toc:end-->

[![Go Reference](https://pkg.go.dev/badge/github.com/catgoose/promolog.svg)](https://pkg.go.dev/github.com/catgoose/promolog)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

![promolog](https://raw.githubusercontent.com/catgoose/screenshots/main/promolog/promolog.png)

Per-request log capture with policy-driven promotion for Go.

> past is already past -- don't debug it
>
> -- Layman Grug

Grug was almost right. But when the request fails, the past is exactly what you need. Promolog says: buffer the past, discard it when it doesn't matter, and promote it when it does.

The mental model is simple: **buffer every request, promote based on rules.** An error is one rule. A slow checkout is another. An admin audit trail is a third. The mechanism is the same -- the policy decides.

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
    slog.Error("database timeout", "err", err)
    // Good luck finding the 5 log lines that led to this.
}
```

**With promolog:**

```go
// Success: logs buffered in memory, then discarded. Zero noise.
// Error: entire request trace promoted to storage. Full context.
// Slow request: promoted too. Admin audit: also promoted.
// The policy decides. You define the rules.

handler := promolog.CorrelationMiddleware(
    promolog.AutoPromoteMiddleware(store,
        promolog.StatusPolicy(500),
        promolog.LatencyPolicy(2 * time.Second),
        promolog.RoutePolicy("/admin/*", func(code int) bool { return true }),
    )(mux),
)
```

During normal requests, log records are buffered in memory and discarded.
When a policy matches, the entire buffer is promoted to a store for
later inspection. You get full request context -- every log line, the request
and response bodies, trace tags, parent request IDs -- without the noise of
successful requests.

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

1. `CorrelationMiddleware` attaches a `Buffer`, request ID, and start time to the context
2. A `slog.Handler` wrapper captures every log record into the buffer
3. `AutoPromoteMiddleware` evaluates promotion policies after the handler returns
4. If any policy matches, the buffer is promoted to the store automatically
5. Query the store later to see exactly what happened

```
request in --> middleware --> buffer logs --> policies match?
                                         \-> no:  discard
                                         \-> yes: promote to store
```

## Quick start

```go
import (
    "database/sql"
    "log/slog"
    "net/http"
    "time"

    "github.com/catgoose/promolog"
    "github.com/catgoose/promolog/sqlite"
    _ "github.com/mattn/go-sqlite3"
)

// 1. Set up the store
db, _ := sql.Open("sqlite3", "traces.db")
store := sqlite.NewStore(db)
store.InitSchema()
store.StartCleanup(ctx, 90*24*time.Hour, time.Hour)

// 2. Wrap your slog handler
logger := slog.New(promolog.NewHandler(slog.Default().Handler()))
slog.SetDefault(logger)

// 3. Define your promotion policies
policies := []promolog.PromotionPolicy{
    promolog.StatusPolicy(500),                        // all server errors
    promolog.LatencyPolicy(2 * time.Second),           // slow requests
    promolog.SamplePolicy(0.01, nil),                  // 1% of everything
    promolog.RoutePolicy("/admin/*", func(int) bool {  // all admin access
        return true
    }),
}

// 4. Wire up the middleware stack
mux := http.NewServeMux()
handler := promolog.CorrelationMiddleware(
    promolog.AutoPromoteMiddleware(store, policies...)(mux),
)
http.ListenAndServe(":8080", handler)
```

> Grug say: "complexity is apex predator." Student say: "how do I defeat the complexity?" Grug say: "no."
>
> -- Layman Grug

Promolog says "no" to log noise. The policy decides what matters. Everything else is discarded.

## Promotion policies

Promotion is policy-driven: a predicate and a name. The default policy is
`status >= 500 -> promote`, but "error" is just one policy. Users define
additional policies for audit trails, slow requests, sampling, and noise
suppression.

### Built-in policies

```go
// Server errors (status >= 500)
promolog.StatusPolicy(500)

// Route-specific: always promote admin access
promolog.RoutePolicy("/admin/*", func(code int) bool { return true })

// Route-specific: promote 4xx+ on API routes
promolog.RoutePolicy("/api/*", func(code int) bool { return code >= 400 })

// Latency: promote anything over 2 seconds
promolog.LatencyPolicy(2 * time.Second)

// Sampling: promote 1% of all requests for baseline visibility
promolog.SamplePolicy(0.01, nil)

// Sampling with deterministic RNG (for tests)
rng := rand.New(rand.NewSource(42))
promolog.SamplePolicy(0.05, rng)
```

Custom policies are just a `PromotionPolicy` struct:

```go
promolog.PromotionPolicy{
    Name: "high-value-user",
    Predicate: func(r *http.Request, statusCode int) bool {
        return r.Header.Get("X-User-Tier") == "enterprise"
    },
}
```

### Auto-promote middleware

`AutoPromoteMiddleware` captures the response status code and evaluates
policies automatically -- no manual `Promote` calls needed:

```go
promolog.AutoPromoteMiddleware(store,
    promolog.StatusPolicy(500),
    promolog.LatencyPolicy(2 * time.Second),
)(mux)
```

The middleware wraps `ResponseWriter` to capture the status code, runs the
downstream handler, then checks each policy. First match wins.

### Runtime filter rules

For rules that change without redeployment, store them in SQLite:

```go
// Create a rule to suppress Chrome DevTools noise
store.CreateRule(ctx, promolog.FilterRule{
    Name:     "devtools noise",
    Field:    "route",
    Operator: "starts_with",
    Value:    "/favicon",
    Action:   "suppress",
    Enabled:  true,
})

// Always capture admin requests
store.CreateRule(ctx, promolog.FilterRule{
    Name:     "admin audit",
    Field:    "route",
    Operator: "matches_glob",
    Value:    "/admin/*",
    Action:   "always_promote",
    Enabled:  true,
})

// Load rules into an engine for fast evaluation
engine, _ := store.LoadRuleEngine(ctx)
action, matched := engine.Match(promolog.TraceFields(trace))
```

Available actions: `suppress`, `always_promote`, `tag`, `short_ttl`.

### Manual promotion

`AutoPromoteMiddleware` handles most cases, but manual `Promote` remains
available as an escape hatch:

```go
buf := promolog.GetBuffer(r.Context())
store.Promote(ctx, promolog.Trace{
    RequestID:  promolog.GetRequestID(r.Context()),
    ErrorChain: err.Error(),
    StatusCode: 500,
    Route:      r.URL.Path,
    Method:     r.Method,
    Entries:    buf.Entries(),
})
```

## Middleware stack

### Correlation

`CorrelationMiddleware` sets up per-request state: a unique request ID, a
`Buffer` for log capture, and a start time for latency tracking.

```go
// Basic usage
handler := promolog.CorrelationMiddleware(mux)

// With a buffer entry limit (prevents memory blowup)
handler := promolog.CorrelationMiddlewareWithLimit(500)(mux)
```

It reads `X-Request-ID` from incoming requests. When present, the incoming ID
becomes the parent and a fresh child ID is generated for this service's span.

### Body capture

`BodyCaptureMiddleware` captures request and response bodies into the buffer
for inclusion in promoted traces:

```go
promolog.BodyCaptureMiddleware(
    promolog.WithMaxBodySize(64 * 1024),    // default: 64 KiB
    promolog.WithRedactor(func(body []byte) []byte {
        // strip sensitive fields before storage
        return redact(body)
    }),
)
```

Bodies are opt-in, truncated to a configurable limit, and support redaction
hooks for stripping passwords, tokens, or PII.

### Buffer limits

A handler logging in a tight loop could buffer thousands of entries. Cap it:

```go
// Via middleware
promolog.CorrelationMiddlewareWithLimit(500)

// Via handler option
promolog.NewHandler(inner, promolog.WithBufferLimit(500))
```

When the limit is exceeded, the buffer keeps the first half and last half of
entries (preserving how the request started and ended) and inserts a synthetic
WARN entry noting how many middle entries were elided.

### Putting it together

```go
mux := http.NewServeMux()

handler := promolog.CorrelationMiddlewareWithLimit(500)(
    promolog.BodyCaptureMiddleware(
        promolog.WithMaxBodySize(32 * 1024),
    )(
        promolog.AutoPromoteMiddleware(store,
            promolog.StatusPolicy(500),
            promolog.LatencyPolicy(2 * time.Second),
        )(mux),
    ),
)
```

Order matters: `Correlation` first (sets up context), then `BodyCapture`
(reads/wraps bodies), then `AutoPromote` (evaluates policies and promotes).

## Distributed tracing

Propagate request IDs across service boundaries:

```go
// Outbound: wrap your HTTP client
client := promolog.NewCorrelatedClient(http.DefaultClient)

// Or wrap just the transport
transport := promolog.CorrelationTransport(http.DefaultTransport)
client := &http.Client{Transport: transport}
```

When service A calls service B, the `X-Request-ID` header is set automatically.
Service B's `CorrelationMiddleware` picks it up as the parent ID and generates
a child ID. The `ParentRequestID` field on `Trace` links them.

```go
// In service B, the trace includes:
trace.ParentRequestID // service A's request ID
trace.RequestID       // service B's own ID
```

## Trace tags

Attach arbitrary key-value tags to the buffer for higher-level categorization:

```go
buf := promolog.GetBuffer(r.Context())
buf.Tag("feature", "checkout")
buf.Tag("tenant", "acme")
buf.Tag("source", "internal")
```

Tags are stored alongside the trace, queryable via `TraceFilter`, and surfaced
in `AvailableFilters` for building filter dropdowns.

## Querying traces

```go
// Get a single trace with full log entries
trace, err := store.Get(ctx, "req-abc-123")

// List traces with filtering, search, sorting, and pagination
rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{
    Q:       "connection",         // full-text search
    Status:  "5xx",                // "4xx", "5xx", or exact like "502"
    Method:  "POST",
    Tags:    map[string]string{    // filter by tags (AND semantics)
        "feature": "checkout",
    },
    Sort:    "StatusCode",         // CreatedAt, StatusCode, Route, Method
    Dir:     "desc",
    Page:    1,
    PerPage: 25,
})

// Delete a trace
store.DeleteTrace(ctx, "req-abc-123")
```

### Aggregation

Group traces by dimension and surface patterns:

```go
results, err := store.Aggregate(ctx, promolog.AggregateFilter{
    GroupBy:  "route",            // "route", "status_code", "method", "error_chain"
    Window:   1 * time.Hour,     // time window
    MinCount: 5,                 // minimum traces per group
})
// [{Key: "/api/users", Count: 47, TopErrors: ["connection refused", "timeout"]}]
```

### Available filters

Build filter dropdowns from actual data:

```go
opts, _ := store.AvailableFilters(ctx, promolog.TraceFilter{})
// opts.StatusCodes: []int{400, 500, 502}
// opts.Methods:     []string{"GET", "POST"}
// opts.Routes:      []string{"/api/users", "/admin/settings"}
// opts.RemoteIPs:   []string{"10.0.0.1", "192.168.1.100"}
// opts.UserIDs:     []string{"user-42", "admin-1"}
// opts.TagKeys:     []string{"feature", "tenant"}
// opts.Tags:        map[string][]string{"feature": ["checkout", "search"]}
```

## Retention policies

Different traces deserve different lifetimes:

```go
// Keep 5xx traces for 90 days
store.CreateRetentionRule(ctx, promolog.RetentionRule{
    Name:     "server errors",
    Field:    "status_code",
    Operator: "starts_with",
    Value:    "5",
    TTLHours: 90 * 24,
    Enabled:  true,
})

// Keep 4xx traces for 7 days
store.CreateRetentionRule(ctx, promolog.RetentionRule{
    Name:     "client errors",
    Field:    "status_code",
    Operator: "starts_with",
    Value:    "4",
    TTLHours: 7 * 24,
    Enabled:  true,
})

// Admin audit trails: 180 days
store.CreateRetentionRule(ctx, promolog.RetentionRule{
    Name:     "admin audit",
    Field:    "route",
    Operator: "starts_with",
    Value:    "/admin",
    TTLHours: 180 * 24,
    Enabled:  true,
})
```

The cleanup goroutine evaluates retention rules per trace. Traces matching a
rule use that rule's TTL. Unmatched traces use the global default. When
multiple rules match, the shortest TTL wins.

## Export adapters

Export promoted traces to external systems without blocking the promote path:

```go
import (
    jsonexport "github.com/catgoose/promolog/export/json"
    "github.com/catgoose/promolog/export/webhook"
)

// Structured JSON lines to stdout
exporter := jsonexport.New(os.Stdout, jsonexport.WithPretty())

// Webhook: POST to an endpoint
exporter := webhook.New("https://example.com/traces",
    webhook.WithTimeout(5 * time.Second),
    webhook.WithHeader("Authorization", "Bearer token"),
)

// Wire it up -- exports run async, never block promotes
promolog.WireExporter(store, exporter, store.Get)
```

The `Exporter` interface is simple:

```go
type Exporter interface {
    Export(ctx context.Context, trace Trace) error
    Close() error
}
```

Write your own for Datadog, Loki, Slack, PagerDuty -- whatever your
infrastructure needs.

## Bring your own store

The core library defines a `Storer` interface. The SQLite implementation lives
in `github.com/catgoose/promolog/sqlite`, but you can implement `Storer` with
any backend:

```go
type Storer interface {
    InitSchema() error
    SetOnPromote(fn func(TraceSummary))
    Promote(ctx context.Context, trace Trace) error
    PromoteAt(ctx context.Context, trace Trace, createdAt time.Time) error
    Get(ctx context.Context, requestID string) (*Trace, error)
    ListTraces(ctx context.Context, f TraceFilter) ([]TraceSummary, int, error)
    AvailableFilters(ctx context.Context, f TraceFilter) (FilterOptions, error)
    DeleteTrace(ctx context.Context, requestID string) error
    StartCleanup(ctx context.Context, ttl time.Duration, interval time.Duration)
    CreateRule(ctx context.Context, rule FilterRule) (FilterRule, error)
    ListRules(ctx context.Context) ([]FilterRule, error)
    UpdateRule(ctx context.Context, rule FilterRule) error
    DeleteRule(ctx context.Context, id int) error
    CreateRetentionRule(ctx context.Context, rule RetentionRule) (RetentionRule, error)
    ListRetentionRules(ctx context.Context) ([]RetentionRule, error)
    UpdateRetentionRule(ctx context.Context, rule RetentionRule) error
    DeleteRetentionRule(ctx context.Context, id int) error
    Aggregate(ctx context.Context, f AggregateFilter) ([]AggregateResult, error)
}
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

## Testing

`Storer` is an interface -- mock it in your application tests.

`SamplePolicy` accepts an optional `*rand.Rand` for deterministic test behavior.
`LatencyPolicy` reads the start time from context, so you can control it in tests
by setting `startTimeKey` via `CorrelationMiddleware`.

## API reference

### Core (`github.com/catgoose/promolog`) -- zero dependencies

| Type / Function                 | Description                                                                          |
| ------------------------------- | ------------------------------------------------------------------------------------ |
| `Storer`                        | Interface for trace persistence                                                      |
| `Exporter`                      | Interface for exporting traces to external systems                                   |
| `Handler`                       | `slog.Handler` wrapper that captures records into a per-request buffer               |
| `NewHandler(inner, ...option)`  | Creates a Handler wrapping an existing slog handler                                  |
| `Buffer`                        | Thread-safe per-request log buffer with optional size limits                         |
| `Trace`                         | Full trace with entries, bodies, tags, and parent request ID                         |
| `TraceSummary`                  | Lightweight trace without entries (for list views)                                   |
| `TraceFilter`                   | Query parameters for `ListTraces` and `AvailableFilters`                            |
| `FilterOptions`                 | Distinct values for filter dropdowns (status, method, route, IP, user, tags)        |
| `FilterRule`                    | Runtime suppress/promote/tag rule stored in the database                            |
| `RuleEngine`                    | In-memory rule evaluator for fast matching                                           |
| `RetentionRule`                 | Per-route/status retention policy with custom TTL                                   |
| `AggregateFilter`               | Grouping parameters for trace aggregation                                           |
| `AggregateResult`               | Aggregation bucket with count and top errors                                        |
| `PromotionPolicy`               | Predicate-based promotion decision (status, route, latency, sample)                 |
| `StatusPolicy(minCode)`         | Promotes when status >= minCode                                                     |
| `RoutePolicy(pattern, fn)`      | Promotes when route matches and fn returns true                                     |
| `LatencyPolicy(threshold)`      | Promotes when request duration exceeds threshold                                    |
| `SamplePolicy(rate, rng)`       | Promotes a random fraction of requests                                              |
| `CorrelationMiddleware`         | Sets up request ID, parent ID, buffer, and start time                               |
| `AutoPromoteMiddleware`         | Evaluates policies and promotes automatically                                       |
| `BodyCaptureMiddleware`         | Captures request/response bodies into the buffer                                    |
| `CorrelationTransport`          | `http.RoundTripper` that propagates request IDs on outbound calls                   |
| `NewCorrelatedClient`           | Creates an `http.Client` with request ID propagation                                |
| `WireExporter`                  | Connects an Exporter to a store's OnPromote callback                                |
| `ErrDuplicateTrace`             | Sentinel error for duplicate request IDs                                            |

### SQLite store (`github.com/catgoose/promolog/sqlite`)

| Type / Function | Description                                                 |
| --------------- | ----------------------------------------------------------- |
| `Store`         | SQLite-backed implementation of `promolog.Storer`           |
| `NewStore(db)`  | Constructor -- pass a `*sql.DB` opened with a SQLite driver |

### Export packages

| Package                                    | Description                                        |
| ------------------------------------------ | -------------------------------------------------- |
| `github.com/catgoose/promolog/export/json` | JSON lines exporter to any `io.Writer`             |
| `github.com/catgoose/promolog/export/webhook` | HTTP POST exporter to a configurable endpoint   |

## Philosophy

> Grug's last teaching: past is already past -- don't debug it. future not here yet -- don't optimize for it. server return html -- this present moment.
>
> -- Layman Grug

Promolog amends the teaching: the past is past -- unless a policy says otherwise. Then the past is exactly what you need, and promolog kept it for you.

> If you are building something that must evolve -- while clients depend on it, while teams change, while requirements shift, while Kevin goes on PTO and comes back and the new Kevin doesn't know the old Kevin's conventions -- then you need an architecture that permits change without breaking the contract.
>
> -- The Wisdom of the Uniform Interface

Promolog is that architecture for your request traces. The policy is the contract. When Kevin comes back from PTO, the full request context is waiting in the store. The admin audit trail is there. The slow checkout that preceded the outage is there. The 1% sample of baseline traffic is there. Kevin doesn't need to know what happened. The store knows what happened.

Promolog follows the [dothog design philosophy](https://github.com/catgoose/dothog/blob/main/PHILOSOPHY.md): zero dependencies in the core, interface-driven extensibility, and the server handles state so you don't have to.

## Architecture

```
  request in ──► CorrelationMiddleware ──► BodyCaptureMiddleware ──► AutoPromoteMiddleware ──► handler
                      │                         │                          │                      │
                      │  attach Buffer,         │  capture req/res         │  evaluate            │  slog calls
                      │  request ID,            │  bodies into             │  policies             │  captured by
                      │  parent ID,             │  Buffer                  │  after handler        │  Handler
                      │  start time             │                          │  returns              │     │
                      │                         │                          │                       │     v
                      │                         │                          │                  ┌─────────┐
                      │                         │                          │                  │ Buffer   │
                      │                         │                          │                  │ (memory) │
                      │                         │                          │                  └────┬─────┘
                      │                         │                          │                       │
                      │                         │                    policy matches?               │
                      │                         │                       │          │               │
                      │                         │                      no         yes              │
                      │                         │                       │          │               │
                      │                         │                    discard    Promote()          │
                      │                         │                                  │               │
                      │                         │                             ┌────v──────┐        │
                      │                         │                             │  Store    │        │
                      │                         │                             │ (SQLite)  │        │
                      │                         │                             └────┬──────┘        │
                      │                         │                                  │               │
                      │                         │                             ┌────v──────┐        │
                      │                         │                             │ Exporters │        │
                      │                         │                             │ (async)   │        │
                      │                         │                             └───────────┘        │
```

## License

MIT
