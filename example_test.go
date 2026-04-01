package promolog_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/catgoose/promolog"
)

func ExampleNewHandler() {
	// Wrap any slog.Handler to capture log records per-request.
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := promolog.NewHandler(inner)
	logger := slog.New(handler)

	// Attach a request ID and buffer to the context.
	ctx := context.WithValue(context.Background(), promolog.RequestIDKey, "req-001")
	ctx = promolog.NewBufferContext(ctx)

	// Log normally; records are captured in the per-request buffer.
	logger.InfoContext(ctx, "handling request", "path", "/api/users")
	logger.DebugContext(ctx, "loaded 42 rows")

	buf := promolog.GetBuffer(ctx)
	fmt.Println(len(buf.Entries()))
	// Output: 2
}

func ExampleCorrelationMiddleware() {
	// CorrelationMiddleware generates a request ID, sets the X-Request-ID
	// header, and initializes a promolog Buffer on each request's context.
	// See the package-level docs for HTTP handler usage.
	fmt.Println("wrap with promolog.CorrelationMiddleware(handler)")
	// Output: wrap with promolog.CorrelationMiddleware(handler)
}

func ExampleGetBuffer() {
	// Attach a buffer to the context.
	ctx := promolog.NewBufferContext(context.Background())

	buf := promolog.GetBuffer(ctx)
	buf.Append(promolog.Entry{
		Time:    time.Now(),
		Level:   "INFO",
		Message: "manual entry",
	})

	fmt.Println(len(buf.Entries()))

	// Without a buffer, GetBuffer returns nil.
	empty := promolog.GetBuffer(context.Background())
	fmt.Println(empty)
	// Output:
	// 1
	// <nil>
}
