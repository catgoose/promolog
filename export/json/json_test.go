package jsonexport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/catgoose/promolog"
	jsonexport "github.com/catgoose/promolog/export/json"
)

func sampleTrace() promolog.Trace {
	return promolog.Trace{
		RequestID:       "req-001",
		ParentRequestID: "parent-xyz",
		ErrorChain:      "something broke",
		StatusCode:      500,
		Route:           "/api/items",
		Method:          "GET",
		UserAgent:       "test-agent",
		RemoteIP:        "127.0.0.1",
		UserID:          "user-42",
		Tags:            map[string]string{"env": "test"},
		Entries: []promolog.Entry{
			{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Level: "ERROR", Message: "boom"},
		},
		RequestBody:  `{"q":"hello"}`,
		ResponseBody: `{"error":"boom"}`,
		CreatedAt:    time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestExport_CompactJSON(t *testing.T) {
	var buf bytes.Buffer
	exp := jsonexport.New(&buf)

	err := exp.Export(context.Background(), sampleTrace())
	require.NoError(t, err)

	line := buf.String()
	assert.True(t, line[len(line)-1] == '\n', "output should end with newline")

	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &m))

	assert.Equal(t, "req-001", m["request_id"])
	assert.Equal(t, float64(500), m["status_code"])
	assert.Equal(t, "/api/items", m["route"])
	assert.Equal(t, "GET", m["method"])
	assert.Equal(t, "something broke", m["error_chain"])
}

func TestExport_PrettyJSON(t *testing.T) {
	var buf bytes.Buffer
	exp := jsonexport.New(&buf, jsonexport.WithPretty())

	err := exp.Export(context.Background(), sampleTrace())
	require.NoError(t, err)

	// Pretty output should contain indentation.
	assert.Contains(t, buf.String(), "  ")

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	assert.Equal(t, "req-001", m["request_id"])
}

func TestExport_WithFields(t *testing.T) {
	var buf bytes.Buffer
	exp := jsonexport.New(&buf, jsonexport.WithFields("request_id", "status_code"))

	err := exp.Export(context.Background(), sampleTrace())
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))

	assert.Equal(t, "req-001", m["request_id"])
	assert.Equal(t, float64(500), m["status_code"])
	// Other fields should be absent.
	assert.NotContains(t, m, "route")
	assert.NotContains(t, m, "method")
	assert.NotContains(t, m, "entries")
	assert.NotContains(t, m, "request_body")
	assert.NotContains(t, m, "response_body")
	assert.NotContains(t, m, "parent_request_id")
}

func TestExport_IncludesParentRequestIDAndBodies(t *testing.T) {
	var buf bytes.Buffer
	exp := jsonexport.New(&buf)

	err := exp.Export(context.Background(), sampleTrace())
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))

	assert.Equal(t, "parent-xyz", m["parent_request_id"])
	assert.Equal(t, `{"q":"hello"}`, m["request_body"])
	assert.Equal(t, `{"error":"boom"}`, m["response_body"])
}

func TestExport_OmitsEmptyParentRequestIDAndBodies(t *testing.T) {
	var buf bytes.Buffer
	exp := jsonexport.New(&buf)

	tr := sampleTrace()
	tr.ParentRequestID = ""
	tr.RequestBody = ""
	tr.ResponseBody = ""
	require.NoError(t, exp.Export(context.Background(), tr))

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))

	assert.NotContains(t, m, "parent_request_id")
	assert.NotContains(t, m, "request_body")
	assert.NotContains(t, m, "response_body")
}

func TestExport_WithFields_FiltersByParentRequestID(t *testing.T) {
	var buf bytes.Buffer
	exp := jsonexport.New(&buf, jsonexport.WithFields("request_id", "parent_request_id"))

	err := exp.Export(context.Background(), sampleTrace())
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))

	assert.Equal(t, "req-001", m["request_id"])
	assert.Equal(t, "parent-xyz", m["parent_request_id"])
	assert.NotContains(t, m, "request_body")
	assert.NotContains(t, m, "response_body")
	assert.NotContains(t, m, "status_code")
}

func TestExport_MultipleTraces(t *testing.T) {
	var buf bytes.Buffer
	exp := jsonexport.New(&buf)

	require.NoError(t, exp.Export(context.Background(), sampleTrace()))
	require.NoError(t, exp.Export(context.Background(), sampleTrace()))

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	assert.Len(t, lines, 2, "should have two JSON lines")
}

func TestExport_IncludesOperationFields(t *testing.T) {
	var buf bytes.Buffer
	exp := jsonexport.New(&buf)

	started := time.Date(2025, 1, 1, 11, 0, 0, 0, time.UTC)
	tr := promolog.Trace{
		Kind:            promolog.TraceKindOperation,
		RequestID:       "op-1",
		OperationID:     "op-1",
		OperationName:   "sales-goals-sync",
		OriginRequestID: "req-origin",
		Status:          promolog.OperationStatusFailed,
		ErrorChain:      "upstream timeout",
		StartedAt:       started,
		Duration:        2 * time.Second,
		CreatedAt:       time.Date(2025, 1, 1, 11, 0, 2, 0, time.UTC),
	}
	require.NoError(t, exp.Export(context.Background(), tr))

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	assert.Equal(t, "operation", m["kind"])
	assert.Equal(t, "op-1", m["operation_id"])
	assert.Equal(t, "sales-goals-sync", m["operation_name"])
	assert.Equal(t, "req-origin", m["origin_request_id"])
	assert.Equal(t, "failed", m["status"])
	assert.Equal(t, float64(2000), m["duration_ms"])
	assert.Equal(t, "2025-01-01T11:00:00Z", m["started_at"])
}

func TestExport_OmitsOperationFieldsForRequestTrace(t *testing.T) {
	var buf bytes.Buffer
	exp := jsonexport.New(&buf)

	require.NoError(t, exp.Export(context.Background(), sampleTrace()))

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))
	assert.NotContains(t, m, "kind")
	assert.NotContains(t, m, "operation_id")
	assert.NotContains(t, m, "operation_name")
	assert.NotContains(t, m, "origin_request_id")
	assert.NotContains(t, m, "status")
	assert.NotContains(t, m, "started_at")
	assert.NotContains(t, m, "duration_ms")
}

func TestClose(t *testing.T) {
	var buf bytes.Buffer
	exp := jsonexport.New(&buf)
	assert.NoError(t, exp.Close())
}
