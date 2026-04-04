// Package sqlite provides a SQLite-backed implementation of [promolog.Storer].
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
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
	tags        TEXT,
	created_at  TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_error_traces_request_id ON error_traces(request_id);
CREATE INDEX IF NOT EXISTS idx_error_traces_created_at ON error_traces(created_at);`

const migrateAddTags = `ALTER TABLE error_traces ADD COLUMN tags TEXT`

// Store is a SQLite-backed store of traces.
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
// It also applies any necessary migrations for existing databases.
func (s *Store) InitSchema() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Migration: add tags column if missing (existing databases).
	// ALTER TABLE ... ADD COLUMN is a no-op error when the column exists.
	if _, err := s.db.Exec(migrateAddTags); err != nil {
		// Ignore "duplicate column" errors — the column already exists.
		if !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	return nil
}

// SetOnPromote registers a callback invoked after each successful promote.
func (s *Store) SetOnPromote(fn func(promolog.TraceSummary)) {
	s.onPromote = fn
}

// Promote persists a trace to the database.
func (s *Store) Promote(ctx context.Context, trace promolog.Trace) error {
	return s.promoteAt(ctx, trace, time.Now().UTC())
}

// PromoteAt persists a trace with a specific timestamp.
func (s *Store) PromoteAt(ctx context.Context, trace promolog.Trace, createdAt time.Time) error {
	return s.promoteAt(ctx, trace, createdAt)
}

func (s *Store) promoteAt(ctx context.Context, trace promolog.Trace, createdAt time.Time) error {
	data, err := json.Marshal(trace.Entries)
	if err != nil {
		return fmt.Errorf("marshal entries: %w", err)
	}
	var tagsJSON *string
	if len(trace.Tags) > 0 {
		tb, err := json.Marshal(trace.Tags)
		if err != nil {
			return fmt.Errorf("marshal tags: %w", err)
		}
		s := string(tb)
		tagsJSON = &s
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO error_traces
			(request_id, error_chain, status_code, route, method, user_agent, remote_ip, user_id, entries, tags, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		trace.RequestID, trace.ErrorChain, trace.StatusCode,
		trace.Route, trace.Method, trace.UserAgent,
		trace.RemoteIP, trace.UserID, string(data),
		tagsJSON, createdAt,
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
			Tags:       trace.Tags,
			CreatedAt:  createdAt,
		})
	}
	return nil
}

// Get returns the full trace for a request ID, or nil if not found.
func (s *Store) Get(ctx context.Context, requestID string) (*promolog.Trace, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT request_id, error_chain, status_code, route, method, user_agent, remote_ip, user_id, entries, tags, created_at
		FROM error_traces WHERE request_id = ?`, requestID)

	var t promolog.Trace
	var entriesJSON string
	var tagsJSON sql.NullString
	err := row.Scan(&t.RequestID, &t.ErrorChain, &t.StatusCode, &t.Route,
		&t.Method, &t.UserAgent, &t.RemoteIP, &t.UserID, &entriesJSON, &tagsJSON, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan trace: %w", err)
	}
	if err := json.Unmarshal([]byte(entriesJSON), &t.Entries); err != nil {
		return nil, fmt.Errorf("unmarshal entries: %w", err)
	}
	if tagsJSON.Valid && tagsJSON.String != "" {
		if err := json.Unmarshal([]byte(tagsJSON.String), &t.Tags); err != nil {
			return nil, fmt.Errorf("unmarshal tags: %w", err)
		}
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
		`SELECT request_id, error_chain, status_code, route, method, remote_ip, user_id, tags, created_at
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
		var tagsJSON sql.NullString
		if err := rows.Scan(&ts.RequestID, &ts.ErrorChain, &ts.StatusCode,
			&ts.Route, &ts.Method, &ts.RemoteIP, &ts.UserID, &tagsJSON, &ts.CreatedAt); err != nil {
			return nil, 0, err
		}
		if tagsJSON.Valid && tagsJSON.String != "" {
			_ = json.Unmarshal([]byte(tagsJSON.String), &ts.Tags)
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

	// Remote IPs (apply all filters)
	allW, allA := buildWhere(f)
	ipRows, err := s.db.QueryContext(ctx, "SELECT DISTINCT remote_ip FROM error_traces"+allW+" ORDER BY remote_ip", allA...)
	if err != nil {
		return opts, err
	}
	defer ipRows.Close()
	for ipRows.Next() {
		var ip sql.NullString
		if err := ipRows.Scan(&ip); err != nil {
			return opts, err
		}
		if ip.Valid && ip.String != "" {
			opts.RemoteIPs = append(opts.RemoteIPs, ip.String)
		}
	}

	// Routes (apply all filters)
	routeRows, err := s.db.QueryContext(ctx, "SELECT DISTINCT route FROM error_traces"+allW+" ORDER BY route", allA...)
	if err != nil {
		return opts, err
	}
	defer routeRows.Close()
	for routeRows.Next() {
		var route string
		if err := routeRows.Scan(&route); err != nil {
			return opts, err
		}
		if route != "" {
			opts.Routes = append(opts.Routes, route)
		}
	}

	// User IDs (apply all filters)
	uidRows, err := s.db.QueryContext(ctx, "SELECT DISTINCT user_id FROM error_traces"+allW+" ORDER BY user_id", allA...)
	if err != nil {
		return opts, err
	}
	defer uidRows.Close()
	for uidRows.Next() {
		var uid sql.NullString
		if err := uidRows.Scan(&uid); err != nil {
			return opts, err
		}
		if uid.Valid && uid.String != "" {
			opts.UserIDs = append(opts.UserIDs, uid.String)
		}
	}

	// Tags — collect distinct keys and distinct values per key from JSON tags column.
	tw, ta := buildWhereExcluding(f, "tags")
	tagRows, err := s.db.QueryContext(ctx, "SELECT tags FROM error_traces"+tw, ta...)
	if err != nil {
		return opts, err
	}
	defer tagRows.Close()
	tagKeySet := make(map[string]struct{})
	tagValues := make(map[string]map[string]struct{})
	for tagRows.Next() {
		var raw sql.NullString
		if err := tagRows.Scan(&raw); err != nil {
			return opts, err
		}
		if !raw.Valid || raw.String == "" {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(raw.String), &m); err != nil {
			continue
		}
		for k, v := range m {
			tagKeySet[k] = struct{}{}
			if tagValues[k] == nil {
				tagValues[k] = make(map[string]struct{})
			}
			tagValues[k][v] = struct{}{}
		}
	}
	if len(tagKeySet) > 0 {
		keys := make([]string, 0, len(tagKeySet))
		for k := range tagKeySet {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		opts.TagKeys = keys

		opts.Tags = make(map[string][]string, len(tagValues))
		for k, vs := range tagValues {
			vals := make([]string, 0, len(vs))
			for v := range vs {
				vals = append(vals, v)
			}
			sort.Strings(vals)
			opts.Tags[k] = vals
		}
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
	addTags(&clauses, &args, f)
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
	if exclude != "tags" {
		addTags(&clauses, &args, f)
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

func addTags(clauses *[]string, args *[]any, f promolog.TraceFilter) {
	for k, v := range f.Tags {
		// Use json_extract to match tag values in the JSON column.
		*clauses = append(*clauses, "json_extract(tags, ?) = ?")
		*args = append(*args, "$."+k, v)
	}
}

func whereString(clauses []string) string {
	if len(clauses) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(clauses, " AND ")
}
