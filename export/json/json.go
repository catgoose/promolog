// Package jsonexport provides a promolog.Exporter that writes traces as JSON
// lines to an io.Writer.
package jsonexport

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/catgoose/promolog"
)

// Option configures the JSON exporter.
type Option func(*Exporter)

// WithPretty enables indented JSON output instead of compact single-line JSON.
func WithPretty() Option {
	return func(e *Exporter) {
		e.pretty = true
	}
}

// WithFields restricts the output to only the specified top-level fields.
// When empty (the default), all fields are included.
func WithFields(fields ...string) Option {
	return func(e *Exporter) {
		e.fields = make(map[string]struct{}, len(fields))
		for _, f := range fields {
			e.fields[f] = struct{}{}
		}
	}
}

var _ promolog.Exporter = (*Exporter)(nil)

// Exporter writes traces as JSON lines to the configured writer.
type Exporter struct {
	w      io.Writer
	mu     sync.Mutex
	pretty bool
	fields map[string]struct{} // nil = all fields
}

// New creates a JSON exporter that writes to w.
func New(w io.Writer, opts ...Option) *Exporter {
	e := &Exporter{w: w}
	for _, o := range opts {
		o(e)
	}
	return e
}

// traceView is the JSON representation written by the exporter. Using a
// dedicated struct lets us add json tags and support field filtering without
// modifying the core Trace type.
type traceView struct {
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

func toView(t promolog.Trace) traceView {
	return traceView{
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

// Export marshals the trace as JSON and writes it as a single line to the
// underlying writer. A newline is appended after each trace.
func (e *Exporter) Export(_ context.Context, trace promolog.Trace) error {
	view := toView(trace)

	var data any = view
	if len(e.fields) > 0 {
		data = filterFields(view, e.fields)
	}

	var b []byte
	var err error
	if e.pretty {
		b, err = json.MarshalIndent(data, "", "  ")
	} else {
		b, err = json.Marshal(data)
	}
	if err != nil {
		return err
	}

	b = append(b, '\n')

	e.mu.Lock()
	defer e.mu.Unlock()
	_, err = e.w.Write(b)
	return err
}

// Close is a no-op for the JSON exporter since it does not own the writer.
func (e *Exporter) Close() error {
	return nil
}

// filterFields converts the view to a map and removes keys not in the allowed
// set. This is intentionally simple — it operates on the JSON-tag names.
func filterFields(v traceView, allowed map[string]struct{}) map[string]any {
	// Marshal then unmarshal to get a map keyed by JSON field names.
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k := range m {
		if _, ok := allowed[k]; !ok {
			delete(m, k)
		}
	}
	return m
}
