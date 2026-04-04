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
		RequestID:  "req-001",
		ErrorChain: "something broke",
		StatusCode: 500,
		Route:      "/api/items",
		Method:     "GET",
		UserAgent:  "test-agent",
		RemoteIP:   "127.0.0.1",
		UserID:     "user-42",
		Tags:       map[string]string{"env": "test"},
		Entries: []promolog.Entry{
			{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Level: "ERROR", Message: "boom"},
		},
		CreatedAt: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
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
}

func TestExport_MultipleTraces(t *testing.T) {
	var buf bytes.Buffer
	exp := jsonexport.New(&buf)

	require.NoError(t, exp.Export(context.Background(), sampleTrace()))
	require.NoError(t, exp.Export(context.Background(), sampleTrace()))

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	assert.Len(t, lines, 2, "should have two JSON lines")
}

func TestClose(t *testing.T) {
	var buf bytes.Buffer
	exp := jsonexport.New(&buf)
	assert.NoError(t, exp.Close())
}
