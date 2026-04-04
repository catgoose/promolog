package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/catgoose/promolog"
	"github.com/catgoose/promolog/sqlite"
	_ "github.com/mattn/go-sqlite3"
)

func ExampleNewStore() {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		panic(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	store := sqlite.NewStore(db)
	if err := store.InitSchema(); err != nil {
		panic(err)
	}

	fmt.Println("store ready")
	// Output: store ready
}

func ExampleStore_Promote() {
	db, _ := sql.Open("sqlite3", ":memory:")
	defer db.Close()
	db.SetMaxOpenConns(1)
	store := sqlite.NewStore(db)
	_ = store.InitSchema()

	ctx := context.Background()

	// Promote persists the buffered log entries when a request fails.
	err := store.Promote(ctx, promolog.ErrorTrace{
		RequestID:  "req-abc-123",
		ErrorChain: "connection refused",
		StatusCode: 502,
		Route:      "/api/users",
		Method:     "GET",
		UserAgent:  "Mozilla/5.0",
		RemoteIP:   "10.0.0.1",
		UserID:     "user-42",
		Entries: []promolog.Entry{
			{Time: time.Now(), Level: "INFO", Message: "starting request"},
			{Time: time.Now(), Level: "ERROR", Message: "connection refused", Attrs: map[string]string{"host": "db.local"}},
		},
	})
	fmt.Println(err)

	// Promoting the same request ID again returns ErrDuplicateTrace.
	err = store.Promote(ctx, promolog.ErrorTrace{
		RequestID:  "req-abc-123",
		ErrorChain: "duplicate",
		StatusCode: 500,
		Route:      "/api/users",
		Method:     "GET",
	})
	fmt.Println(errors.Is(err, promolog.ErrDuplicateTrace))
	// Output:
	// <nil>
	// true
}

func ExampleStore_Get() {
	db, _ := sql.Open("sqlite3", ":memory:")
	defer db.Close()
	db.SetMaxOpenConns(1)
	store := sqlite.NewStore(db)
	_ = store.InitSchema()

	ctx := context.Background()
	_ = store.Promote(ctx, promolog.ErrorTrace{
		RequestID:  "req-lookup",
		ErrorChain: "timeout",
		StatusCode: 504,
		Route:      "/api/orders",
		Method:     "POST",
		Entries: []promolog.Entry{
			{Time: time.Now(), Level: "ERROR", Message: "upstream timeout"},
		},
	})

	// Retrieve the full trace by request ID.
	trace, err := store.Get(ctx, "req-lookup")
	if err != nil {
		panic(err)
	}
	fmt.Println(trace.Route, trace.StatusCode)
	fmt.Println(len(trace.Entries))

	// Non-existent request IDs return nil without error.
	missing, err := store.Get(ctx, "does-not-exist")
	fmt.Println(missing, err)
	// Output:
	// /api/orders 504
	// 1
	// <nil> <nil>
}

func ExampleStore_ListTraces() {
	db, _ := sql.Open("sqlite3", ":memory:")
	defer db.Close()
	db.SetMaxOpenConns(1)
	store := sqlite.NewStore(db)
	_ = store.InitSchema()

	ctx := context.Background()
	_ = store.Promote(ctx, promolog.ErrorTrace{
		RequestID: "req-1", ErrorChain: "not found", StatusCode: 404,
		Route: "/api/users/99", Method: "GET",
	})
	_ = store.Promote(ctx, promolog.ErrorTrace{
		RequestID: "req-2", ErrorChain: "db down", StatusCode: 500,
		Route: "/api/orders", Method: "POST",
	})
	_ = store.Promote(ctx, promolog.ErrorTrace{
		RequestID: "req-3", ErrorChain: "bad gateway", StatusCode: 502,
		Route: "/api/payments", Method: "POST",
	})

	// Filter by status class and method.
	rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{
		Status:  "5xx",
		Method:  "POST",
		Sort:    "StatusCode",
		Dir:     "asc",
		Page:    1,
		PerPage: 10,
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("total:", total)
	for _, r := range rows {
		fmt.Printf("%s %d %s\n", r.Route, r.StatusCode, r.Method)
	}
	// Output:
	// total: 2
	// /api/orders 500 POST
	// /api/payments 502 POST
}

func ExampleStore_AvailableFilters() {
	db, _ := sql.Open("sqlite3", ":memory:")
	defer db.Close()
	db.SetMaxOpenConns(1)
	store := sqlite.NewStore(db)
	_ = store.InitSchema()

	ctx := context.Background()
	_ = store.Promote(ctx, promolog.ErrorTrace{
		RequestID: "req-1", StatusCode: 400, Route: "/a", Method: "GET",
		ErrorChain: "bad request",
	})
	_ = store.Promote(ctx, promolog.ErrorTrace{
		RequestID: "req-2", StatusCode: 500, Route: "/b", Method: "POST",
		ErrorChain: "internal error",
	})

	// Get distinct values for building filter UI dropdowns.
	opts, err := store.AvailableFilters(ctx, promolog.TraceFilter{})
	if err != nil {
		panic(err)
	}
	fmt.Println("codes:", opts.StatusCodes)
	fmt.Println("methods:", opts.Methods)
	// Output:
	// codes: [400 500]
	// methods: [GET POST]
}

func ExampleStore_SetOnPromote() {
	db, _ := sql.Open("sqlite3", ":memory:")
	defer db.Close()
	db.SetMaxOpenConns(1)
	store := sqlite.NewStore(db)
	_ = store.InitSchema()

	// Register a callback for real-time notifications (SSE, webhooks, etc.).
	store.SetOnPromote(func(ts promolog.TraceSummary) {
		fmt.Printf("alert: %s %d %s\n", ts.RequestID, ts.StatusCode, ts.Route)
	})

	ctx := context.Background()
	_ = store.Promote(ctx, promolog.ErrorTrace{
		RequestID:  "req-notify",
		ErrorChain: "something broke",
		StatusCode: 500,
		Route:      "/api/webhook",
		Method:     "POST",
	})
	// Output: alert: req-notify 500 /api/webhook
}

func ExampleStore_DeleteTrace() {
	db, _ := sql.Open("sqlite3", ":memory:")
	defer db.Close()
	db.SetMaxOpenConns(1)
	store := sqlite.NewStore(db)
	_ = store.InitSchema()

	ctx := context.Background()
	_ = store.Promote(ctx, promolog.ErrorTrace{
		RequestID: "req-delete-me", ErrorChain: "gone", StatusCode: 500,
		Route: "/api/test", Method: "DELETE",
	})

	err := store.DeleteTrace(ctx, "req-delete-me")
	fmt.Println("delete err:", err)

	trace, _ := store.Get(ctx, "req-delete-me")
	fmt.Println("after delete:", trace)
	// Output:
	// delete err: <nil>
	// after delete: <nil>
}
