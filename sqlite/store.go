// Package sqlite provides a SQLite-backed implementation of promolog.Storer.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/catgoose/promolog"
)

const schema = `CREATE TABLE IF NOT EXISTS error_traces (
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
);
CREATE INDEX IF NOT EXISTS idx_error_traces_request_id ON error_traces(request_id);
CREATE INDEX IF NOT EXISTS idx_error_traces_created_at ON error_traces(created_at);`

// Store is a SQLite-backed store of error traces.
type Store struct {
	db        *sql.DB
	onPromote func(promolog.TraceSummary)
}

// compile-time check
var _ promolog.Storer = (*Store)(nil)

// NewStore creates a Store backed by the given database connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// InitSchema creates the error_traces table if it doesn't exist.
func (s *Store) InitSchema() error {
	_, err := s.db.Exec(schema)
	return err
}

// SetOnPromote registers a callback invoked after each successful promote.
func (s *Store) SetOnPromote(fn func(promolog.TraceSummary)) {
	s.onPromote = fn
}

// Promote persists an error trace to the database.
func (s *Store) Promote(ctx context.Context, trace promolog.ErrorTrace) error {
	return s.promoteAt(ctx, trace, time.Now().UTC())
}

// PromoteAt persists an error trace with a specific timestamp.
func (s *Store) PromoteAt(ctx context.Context, trace promolog.ErrorTrace, createdAt time.Time) error {
	return s.promoteAt(ctx, trace, createdAt)
}

func (s *Store) promoteAt(ctx context.Context, trace promolog.ErrorTrace, createdAt time.Time) error {
	data, err := json.Marshal(trace.Entries)
	if err != nil {
		return fmt.Errorf("marshal entries: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO error_traces
			(request_id, error_chain, status_code, route, method, user_agent, remote_ip, user_id, entries, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		trace.RequestID, trace.ErrorChain, trace.StatusCode,
		trace.Route, trace.Method, trace.UserAgent,
		trace.RemoteIP, trace.UserID, string(data),
		createdAt,
	)
	if err != nil {
		return fmt.Errorf("insert trace: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return promolog.ErrDuplicateTrace
	}
	if s.onPromote != nil {
		s.onPromote(promolog.TraceSummary{
			RequestID:  trace.RequestID,
			ErrorChain: trace.ErrorChain,
			StatusCode: trace.StatusCode,
			Route:      trace.Route,
			Method:     trace.Method,
			RemoteIP:   trace.RemoteIP,
			UserID:     trace.UserID,
			CreatedAt:  createdAt,
		})
	}
	return nil
}

// Get returns the full error trace for a request ID, or nil if not found.
func (s *Store) Get(ctx context.Context, requestID string) (*promolog.ErrorTrace, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT request_id, error_chain, status_code, route, method, user_agent, remote_ip, user_id, entries, created_at
		FROM error_traces WHERE request_id = ?`, requestID)

	var t promolog.ErrorTrace
	var entriesJSON string
	err := row.Scan(&t.RequestID, &t.ErrorChain, &t.StatusCode, &t.Route,
		&t.Method, &t.UserAgent, &t.RemoteIP, &t.UserID, &entriesJSON, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan trace: %w", err)
	}
	if err := json.Unmarshal([]byte(entriesJSON), &t.Entries); err != nil {
		return nil, fmt.Errorf("unmarshal entries: %w", err)
	}
	return &t, nil
}

// ListTraces returns a page of trace summaries matching the given filters.
func (s *Store) ListTraces(ctx context.Context, f promolog.TraceFilter) ([]promolog.TraceSummary, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PerPage < 1 {
		f.PerPage = 25
	}

	where, args := buildWhere(f)

	// Count
	var total int
	countQ := "SELECT COUNT(*) FROM error_traces" + where
	if err := s.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count: %w", err)
	}

	// Sort
	orderCol := "created_at"
	validSorts := map[string]string{
		"CreatedAt": "created_at", "StatusCode": "status_code",
		"Route": "route", "Method": "method",
	}
	if col, ok := validSorts[f.Sort]; ok {
		orderCol = col
	}
	orderDir := "DESC"
	if f.Dir == "asc" {
		orderDir = "ASC"
	}

	offset := (f.Page - 1) * f.PerPage
	dataQ := fmt.Sprintf(
		`SELECT request_id, error_chain, status_code, route, method, remote_ip, user_id, created_at
		FROM error_traces%s ORDER BY %s %s LIMIT ? OFFSET ?`,
		where, orderCol, orderDir)
	// Use a new slice to avoid aliasing the args backing array.
	dataArgs := make([]any, len(args), len(args)+2)
	copy(dataArgs, args)
	dataArgs = append(dataArgs, f.PerPage, offset)

	rows, err := s.db.QueryContext(ctx, dataQ, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	var result []promolog.TraceSummary
	for rows.Next() {
		var ts promolog.TraceSummary
		if err := rows.Scan(&ts.RequestID, &ts.ErrorChain, &ts.StatusCode,
			&ts.Route, &ts.Method, &ts.RemoteIP, &ts.UserID, &ts.CreatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, ts)
	}
	return result, total, rows.Err()
}

// AvailableFilters returns distinct status codes and methods for filter dropdowns.
func (s *Store) AvailableFilters(ctx context.Context, f promolog.TraceFilter) (promolog.FilterOptions, error) {
	var opts promolog.FilterOptions

	// Status codes (filtered by search + method, not status itself)
	sw, sa := buildWhereExcluding(f, "status")
	codeRows, err := s.db.QueryContext(ctx, "SELECT DISTINCT status_code FROM error_traces"+sw+" ORDER BY status_code", sa...)
	if err != nil {
		return opts, err
	}
	defer codeRows.Close()
	for codeRows.Next() {
		var code int
		if err := codeRows.Scan(&code); err != nil {
			return opts, err
		}
		opts.StatusCodes = append(opts.StatusCodes, code)
	}

	// Methods (filtered by search + status, not method itself)
	mw, ma := buildWhereExcluding(f, "method")
	methodRows, err := s.db.QueryContext(ctx, "SELECT DISTINCT method FROM error_traces"+mw+" ORDER BY method", ma...)
	if err != nil {
		return opts, err
	}
	defer methodRows.Close()
	for methodRows.Next() {
		var m string
		if err := methodRows.Scan(&m); err != nil {
			return opts, err
		}
		opts.Methods = append(opts.Methods, m)
	}
	return opts, nil
}

// DeleteTrace removes a single trace by request ID.
func (s *Store) DeleteTrace(ctx context.Context, requestID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM error_traces WHERE request_id = ?", requestID)
	return err
}

// StartCleanup runs a background goroutine that deletes entries older than ttl.
func (s *Store) StartCleanup(ctx context.Context, ttl, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().Add(-ttl)
				_, _ = s.db.ExecContext(ctx, "DELETE FROM error_traces WHERE created_at < ?", cutoff)
			}
		}
	}()
}

// --- WHERE builders ---

func buildWhere(f promolog.TraceFilter) (where string, args []any) {
	var clauses []string
	addSearch(&clauses, &args, f)
	addStatus(&clauses, &args, f)
	addMethod(&clauses, &args, f)
	return whereString(clauses), args
}

func buildWhereExcluding(f promolog.TraceFilter, exclude string) (where string, args []any) {
	var clauses []string
	addSearch(&clauses, &args, f)
	if exclude != "status" {
		addStatus(&clauses, &args, f)
	}
	if exclude != "method" {
		addMethod(&clauses, &args, f)
	}
	return whereString(clauses), args
}

// escapeLike escapes SQL LIKE metacharacters (%, _) so they match literally.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func addSearch(clauses *[]string, args *[]any, f promolog.TraceFilter) {
	if f.Q == "" {
		return
	}
	for _, term := range strings.Fields(f.Q) {
		pattern := "%" + escapeLike(term) + "%"
		*clauses = append(*clauses,
			"(route LIKE ? ESCAPE '\\' OR error_chain LIKE ? ESCAPE '\\' OR request_id LIKE ? ESCAPE '\\' OR user_id LIKE ? ESCAPE '\\' OR remote_ip LIKE ? ESCAPE '\\' OR method LIKE ? ESCAPE '\\')")
		*args = append(*args, pattern, pattern, pattern, pattern, pattern, pattern)
	}
}

func addStatus(clauses *[]string, args *[]any, f promolog.TraceFilter) {
	if f.Status == "" {
		return
	}
	switch f.Status {
	case "4xx":
		*clauses = append(*clauses, "status_code >= 400 AND status_code < 500")
	case "5xx":
		*clauses = append(*clauses, "status_code >= 500")
	default:
		var code int
		if _, err := fmt.Sscanf(f.Status, "%d", &code); err == nil && code > 0 {
			*clauses = append(*clauses, "status_code = ?")
			*args = append(*args, code)
		}
	}
}

func addMethod(clauses *[]string, args *[]any, f promolog.TraceFilter) {
	if f.Method != "" {
		*clauses = append(*clauses, "method = ?")
		*args = append(*args, f.Method)
	}
}

func whereString(clauses []string) string {
	if len(clauses) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(clauses, " AND ")
}
