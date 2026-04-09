package webhook_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/catgoose/promolog"
	"github.com/catgoose/promolog/export/webhook"
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

func TestExport_Success(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := webhook.New(srv.URL)
	err := exp.Export(context.Background(), sampleTrace())
	require.NoError(t, err)

	assert.Equal(t, "req-001", received["request_id"])
	assert.Equal(t, float64(500), received["status_code"])
	assert.Equal(t, "/api/items", received["route"])
}

func TestExport_CustomHeaders(t *testing.T) {
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := webhook.New(srv.URL, webhook.WithHeader("Authorization", "Bearer tok-123"))
	err := exp.Export(context.Background(), sampleTrace())
	require.NoError(t, err)

	assert.Equal(t, "Bearer tok-123", authHeader)
}

func TestExport_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	exp := webhook.New(srv.URL)
	err := exp.Export(context.Background(), sampleTrace())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestExport_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := webhook.New(srv.URL, webhook.WithTimeout(50*time.Millisecond))
	err := exp.Export(context.Background(), sampleTrace())
	require.Error(t, err)
}

func TestExport_ConnectionRefused(t *testing.T) {
	exp := webhook.New("http://127.0.0.1:1") // nothing listening
	err := exp.Export(context.Background(), sampleTrace())
	require.Error(t, err)
}

func TestClose(t *testing.T) {
	exp := webhook.New("http://example.com")
	assert.NoError(t, exp.Close())
}

func TestExport_IncludesParentRequestIDAndBodies(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := webhook.New(srv.URL)
	err := exp.Export(context.Background(), sampleTrace())
	require.NoError(t, err)

	assert.Equal(t, "parent-xyz", received["parent_request_id"])
	assert.Equal(t, `{"q":"hello"}`, received["request_body"])
	assert.Equal(t, `{"error":"boom"}`, received["response_body"])
}

func TestExport_OmitsEmptyParentRequestIDAndBodies(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := sampleTrace()
	tr.ParentRequestID = ""
	tr.RequestBody = ""
	tr.ResponseBody = ""

	exp := webhook.New(srv.URL)
	require.NoError(t, exp.Export(context.Background(), tr))

	assert.NotContains(t, received, "parent_request_id")
	assert.NotContains(t, received, "request_body")
	assert.NotContains(t, received, "response_body")
}

func TestWithClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	custom := &http.Client{Timeout: 10 * time.Second}
	exp := webhook.New(srv.URL, webhook.WithClient(custom))
	err := exp.Export(context.Background(), sampleTrace())
	require.NoError(t, err)
}
