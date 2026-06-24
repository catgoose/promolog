package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/catgoose/promolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleOperationTrace(opID string) promolog.Trace {
	now := time.Now()
	return promolog.Trace{
		RequestID:       opID,
		Kind:            promolog.TraceKindOperation,
		OperationID:     opID,
		OperationName:   "sales-goals-sync",
		OriginRequestID: "req-origin",
		ParentRequestID: "req-parent",
		Status:          promolog.OperationStatusFailed,
		ErrorChain:      "upstream timeout",
		Tags:            map[string]string{"trigger": "manual"},
		Entries: []promolog.Entry{
			{Time: now, Level: "ERROR", Message: "sync failed"},
		},
		StartedAt: now.Add(-2 * time.Second),
		Duration:  2 * time.Second,
	}
}

func TestPromoteOperation_Roundtrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	tr := sampleOperationTrace("op-1")
	require.NoError(t, store.Promote(ctx, tr))

	got, err := store.Get(ctx, "op-1")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, promolog.TraceKindOperation, got.Kind)
	assert.Equal(t, "op-1", got.OperationID)
	assert.Equal(t, "sales-goals-sync", got.OperationName)
	assert.Equal(t, "req-origin", got.OriginRequestID)
	assert.Equal(t, "req-parent", got.ParentRequestID)
	assert.Equal(t, promolog.OperationStatusFailed, got.Status)
	assert.Equal(t, "upstream timeout", got.ErrorChain)
	assert.Equal(t, "manual", got.Tags["trigger"])
	assert.Equal(t, 2*time.Second, got.Duration)
	assert.WithinDuration(t, tr.StartedAt, got.StartedAt, time.Second)
	require.Len(t, got.Entries, 1)
	assert.Equal(t, "sync failed", got.Entries[0].Message)
}

func TestPromoteOperation_DuplicateOperationID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleOperationTrace("op-dup")))

	err := store.Promote(ctx, sampleOperationTrace("op-dup"))
	assert.True(t, errors.Is(err, promolog.ErrDuplicateTrace))
}

func TestListTraces_DistinguishesOperationFromRequest(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Promote(ctx, sampleTrace("req-1", 500, "GET")))
	require.NoError(t, store.Promote(ctx, sampleOperationTrace("op-1")))

	rows, total, err := store.ListTraces(ctx, promolog.TraceFilter{Page: 1, PerPage: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total)

	byID := make(map[string]promolog.TraceSummary, len(rows))
	for _, r := range rows {
		byID[r.RequestID] = r
	}

	op := byID["op-1"]
	assert.Equal(t, promolog.TraceKindOperation, op.Kind)
	assert.Equal(t, "op-1", op.OperationID)
	assert.Equal(t, "sales-goals-sync", op.OperationName)
	assert.Equal(t, "req-origin", op.OriginRequestID)
	assert.Equal(t, promolog.OperationStatusFailed, op.Status)

	req := byID["req-1"]
	assert.Empty(t, req.Kind)
	assert.Empty(t, req.OperationID)
}

func TestEnsureSchema_MigratesLegacyTable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	legacy := `CREATE TABLE error_traces (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		request_id  VARCHAR(64) NOT NULL UNIQUE,
		error_chain TEXT NOT NULL,
		status_code INT NOT NULL,
		route       VARCHAR(500) NOT NULL,
		method      VARCHAR(10) NOT NULL,
		user_agent  TEXT,
		remote_ip   VARCHAR(45),
		user_id     VARCHAR(255),
		entries     TEXT NOT NULL,
		created_at  TIMESTAMP NOT NULL
	)`
	_, err := db.Exec(legacy)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO error_traces (request_id, error_chain, status_code, route, method, user_agent, remote_ip, user_id, entries, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"legacy-1", "boom", 500, "/old", "GET", "", "", "", "[]", time.Now(),
	)
	require.NoError(t, err)

	store := NewStore(db)
	require.NoError(t, store.EnsureSchema())

	got, err := store.Get(ctx, "legacy-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "boom", got.ErrorChain)
	assert.Empty(t, got.Kind)

	require.NoError(t, store.Promote(ctx, sampleOperationTrace("op-after-migrate")))
	op, err := store.Get(ctx, "op-after-migrate")
	require.NoError(t, err)
	require.NotNil(t, op)
	assert.Equal(t, promolog.TraceKindOperation, op.Kind)
	assert.Equal(t, "sales-goals-sync", op.OperationName)
}
