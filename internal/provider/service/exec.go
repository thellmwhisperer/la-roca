package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/jsonid"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

// ExecRequest is a SELECT the caller wants to run as it is. It is the natural
// companion of `playground --sql-only`.
type ExecRequest struct {
	SQL        string
	MaxChars   int
	Timeout    time.Duration
	TimeoutSet bool
}

// ExecResult is what that SELECT returned, with the SQL that actually ran.
type ExecResult struct {
	SQL              string           `json:"sql"`
	Columns          []string         `json:"columns,omitempty"`
	Rows             []map[string]any `json:"rows,omitempty"`
	RowCount         int              `json:"row_count"`
	MaxChars         int              `json:"-"`
	Databases        []string         `json:"databases,omitempty"`
	OmittedDatabases []string         `json:"omitted_databases,omitempty"`
	LatencyMS        int64            `json:"latency_ms"`
	Version          string           `json:"version"`
	SourceSHA        string           `json:"source_sha"`
}

// Exec validates and runs a SELECT. What does not pass the gate does not touch
// the database, and what does pass runs over a connection on which the engine
// itself rejects any write.
func (s *Service) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	start := time.Now()
	maxChars := TextBudget(req.MaxChars)
	if _, err := s.EnsureSchema(ctx); err != nil {
		return ExecResult{}, err
	}
	route := s.pluginsForSQL(ctx, req.SQL)
	defer route.CloseOnDemand()
	if len(route.Omitted) > 0 {
		return ExecResult{}, logfile.Typed(fmt.Errorf(
			"the SELECT references more than SQLite's %d attached databases; split the query (omitted: %s)",
			plugin.MaxAttached, strings.Join(route.OmittedSources(), ", ")), DegradedInvalidSQL)
	}
	gate, closeGate, err := s.GateFor(route.IncludeCore, route.Databases)
	if err != nil {
		return ExecResult{}, err
	}
	defer closeGate()
	// The stage that failed is only knowable here, and it is the same
	// distinction the degraded answers already declare: what the gate refused
	// and what the engine could not run are two different fixes.
	if err := gate.RejectUnqualified(req.SQL); err != nil {
		return ExecResult{}, logfile.Typed(err, DegradedInvalidSQL)
	}
	validated, err := gate.Validate(req.SQL)
	if err != nil {
		return ExecResult{}, logfile.Typed(err, DegradedInvalidSQL)
	}
	columns, rows, err := s.executeWithPluginsBudget(ctx, validated, "", maxChars, route.Databases,
		execBudget{timeout: req.Timeout, set: req.TimeoutSet})
	if err != nil {
		degraded := DegradedExecution
		if errors.Is(err, ErrQueryTimeout) {
			degraded = DegradedTimeout
		}
		return ExecResult{}, logfile.Typed(err, degraded)
	}
	result := ExecResult{
		SQL:       validated,
		Columns:   columns,
		Rows:      rows,
		RowCount:  len(rows),
		MaxChars:  maxChars,
		LatencyMS: time.Since(start).Milliseconds(),
		Version:   s.opts.Version,
		SourceSHA: s.opts.Commit,
	}
	if s.PluginsActive() {
		result.Databases = route.Consulted()
	}
	return result, nil
}

// execute runs the validated SELECT and normalizes the rows into maps keyed by
// column name, which is what both surfaces render.
func (s *Service) execute(ctx context.Context, stmt, term string, maxChars int) ([]string, []map[string]any, error) {
	return s.ExecuteWithPlugins(ctx, stmt, term, maxChars, nil)
}

// queryExecutionBudget is how long validated SQL may run, and whether it is
// bounded at all. A positive budget is the one that was asked for; an explicit
// zero is the operator removing the bound on purpose; anything else, including
// a value no statement could meet, falls back to the shipped default.
func (s *Service) queryExecutionBudget() (time.Duration, bool) {
	if s.opts.QueryTimeout > 0 {
		return s.opts.QueryTimeout, true
	}
	if s.opts.QueryTimeoutSet && s.opts.QueryTimeout == 0 {
		return 0, false
	}
	return DefaultQueryTimeout, true
}

func executionError(parent, queryCtx context.Context, timeout time.Duration, err error) error {
	if parent.Err() == nil && errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w after %s", ErrQueryTimeout, timeout)
	}
	return fmt.Errorf("run the validated query: %w", err)
}

// ScanRows turns any result set into its column names and its rows of named
// values under the text budget. The query cascade, health diagnosis and
// in-memory cross queries share it, so unexpected column types are handled in
// one place.
func ScanRows(rows *sql.Rows, maxChars int, term string) ([]string, []map[string]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var result []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, nil, err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = values[i]
			switch text := values[i].(type) {
			case []byte:
				row[column] = Truncate(string(text), maxChars, term)
			case string:
				if !jsonid.IdentityName(column) {
					row[column] = Truncate(text, maxChars, term)
				}
			}
			row[column] = jsonid.Cell(column, row[column])
		}
		result = append(result, row)
	}
	return columns, result, rows.Err()
}
