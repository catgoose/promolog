// Package webhook provides a promolog.Exporter that POSTs traces as JSON to a
// configurable HTTP endpoint.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/catgoose/promolog"
)

// Option configures the webhook exporter.
type Option func(*Exporter)

// WithTimeout sets the HTTP request timeout. The default is 5 seconds.
func WithTimeout(d time.Duration) Option {
	return func(e *Exporter) {
		e.client.Timeout = d
	}
}

// WithHeader adds a custom HTTP header to every request. Call multiple times
// to set multiple headers.
func WithHeader(key, value string) Option {
	return func(e *Exporter) {
		e.headers[key] = value
	}
}

// WithClient replaces the default http.Client entirely. When used, WithTimeout
// has no effect.
func WithClient(c *http.Client) Option {
	return func(e *Exporter) {
		e.client = c
	}
}

var _ promolog.Exporter = (*Exporter)(nil)

// Exporter sends traces as JSON to a webhook URL via HTTP POST.
type Exporter struct {
	url     string
	client  *http.Client
	headers map[string]string
}

// New creates a webhook exporter that POSTs to the given URL.
func New(url string, opts ...Option) *Exporter {
	e := &Exporter{
		url: url,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
		headers: make(map[string]string),
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// tracePayload mirrors the JSON structure used by the JSON exporter for
// consistency.
type tracePayload struct {
	RequestID  string            `json:"request_id"`
	ErrorChain string            `json:"error_chain,omitempty"`
	StatusCode int               `json:"status_code"`
	Route      string            `json:"route"`
	Method     string            `json:"method"`
	UserAgent  string            `json:"user_agent,omitempty"`
	RemoteIP   string            `json:"remote_ip,omitempty"`
	UserID     string            `json:"user_id,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Entries    []promolog.Entry  `json:"entries,omitempty"`
	CreatedAt  string            `json:"created_at"`
}

func toPayload(t promolog.Trace) tracePayload {
	return tracePayload{
		RequestID:  t.RequestID,
		ErrorChain: t.ErrorChain,
		StatusCode: t.StatusCode,
		Route:      t.Route,
		Method:     t.Method,
		UserAgent:  t.UserAgent,
		RemoteIP:   t.RemoteIP,
		UserID:     t.UserID,
		Tags:       t.Tags,
		Entries:    t.Entries,
		CreatedAt:  t.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// Export sends the trace as a JSON POST request to the configured URL. The
// provided context is passed through to the HTTP request.
func (e *Exporter) Export(ctx context.Context, trace promolog.Trace) error {
	body, err := json.Marshal(toPayload(trace))
	if err != nil {
		return fmt.Errorf("webhook: marshal trace: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook: server returned %d", resp.StatusCode)
	}

	return nil
}

// Close is a no-op. The exporter does not hold persistent connections.
func (e *Exporter) Close() error {
	return nil
}
