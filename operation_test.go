package promolog

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartOperation_PreservesRequestCorrelation(t *testing.T) {
	ctx := context.WithValue(context.Background(), RequestIDKey, "req-99")
	ctx = context.WithValue(ctx, parentRequestIDKey, "req-parent")

	_, op := StartOperation(ctx, "sales-goals-sync", Attr("trigger", "manual"))

	assert.NotEmpty(t, op.ID())
	assert.Equal(t, "sales-goals-sync", op.Name())
	assert.Equal(t, "req-99", op.originRequestID)
	assert.Equal(t, "req-parent", op.parentRequestID)
}

func TestStartOperation_CapturesLogsAfterCancellationDetached(t *testing.T) {
	logger := slog.New(NewHandler(&discardHandler{}))

	reqCtx := context.WithValue(context.Background(), RequestIDKey, "req-1")
	reqCtx, cancel := context.WithCancel(reqCtx)

	opCtx, op := StartOperation(reqCtx, "sync-job")
	detached := context.WithoutCancel(opCtx)

	// The originating HTTP request finishes and its context is cancelled.
	cancel()

	logger.InfoContext(detached, "background work started", "step", "1")
	logger.ErrorContext(detached, "background work failed")

	entries := op.buf.Entries()
	require.Len(t, entries, 2)
	assert.Equal(t, "background work started", entries[0].Message)
	assert.Equal(t, "1", entries[0].Attrs["step"])
	assert.Equal(t, "background work failed", entries[1].Message)
}

func TestStartOperation_CapturesLogsWithoutRequestContext(t *testing.T) {
	logger := slog.New(NewHandler(&discardHandler{}))

	opCtx, op := StartOperation(context.Background(), "cron-job")
	logger.InfoContext(opCtx, "tick")

	require.Len(t, op.buf.Entries(), 1)
	assert.Empty(t, op.originRequestID)
}

func TestPromoteOperation_BuildsFailedOperationTrace(t *testing.T) {
	store := &mockStorer{}
	logger := slog.New(NewHandler(&discardHandler{}))

	reqCtx := context.WithValue(context.Background(), RequestIDKey, "req-99")
	opCtx, op := StartOperation(reqCtx, "sales-goals-sync", Attr("trigger", "manual"))
	detached := context.WithoutCancel(opCtx)
	logger.InfoContext(detached, "starting sync")

	require.NoError(t, PromoteOperation(detached, store, errors.New("upstream 500")))

	traces := store.promoted()
	require.Len(t, traces, 1)
	tr := traces[0]
	assert.Equal(t, TraceKindOperation, tr.Kind)
	assert.Equal(t, op.ID(), tr.OperationID)
	assert.Equal(t, op.ID(), tr.RequestID)
	assert.Equal(t, "sales-goals-sync", tr.OperationName)
	assert.Equal(t, "req-99", tr.OriginRequestID)
	assert.Equal(t, OperationStatusFailed, tr.Status)
	assert.Equal(t, "upstream 500", tr.ErrorChain)
	assert.Equal(t, "manual", tr.Tags["trigger"])
	assert.GreaterOrEqual(t, tr.Duration, time.Duration(0))
	require.Len(t, tr.Entries, 1)
	assert.Equal(t, "starting sync", tr.Entries[0].Message)
}

func TestPromoteOperation_NilErrorRecordsOK(t *testing.T) {
	store := &mockStorer{}
	opCtx, _ := StartOperation(context.Background(), "cron-job")

	require.NoError(t, PromoteOperation(opCtx, store, nil))

	traces := store.promoted()
	require.Len(t, traces, 1)
	assert.Equal(t, OperationStatusOK, traces[0].Status)
	assert.Empty(t, traces[0].ErrorChain)
}

func TestPromoteOperation_NoOperationInContext(t *testing.T) {
	err := PromoteOperation(context.Background(), &mockStorer{}, nil)
	assert.ErrorIs(t, err, ErrNoOperation)
}

func TestStartOperation_BufferTagsMergeWithAttrs(t *testing.T) {
	store := &mockStorer{}
	opCtx, _ := StartOperation(context.Background(), "job", Attr("trigger", "manual"))
	GetBuffer(opCtx).Tag("tenant", "acme")

	require.NoError(t, PromoteOperation(opCtx, store, nil))

	tags := store.promoted()[0].Tags
	assert.Equal(t, "manual", tags["trigger"])
	assert.Equal(t, "acme", tags["tenant"])
}
