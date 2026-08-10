package search

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

// Provenance says how the search was done. It is not decoration: a poor result
// over the index and a poor one over a LIKE
// are fixed in different ways, and without this the operator does not know which
// of the two they are looking at.
type Provenance struct {
	Method string `json:"search_method"`
	// DegradedFrom names the method that was asked for when it could not be
	// served. A method that degrades silently turns "the index found nothing"
	// into "the index did not run", and those are two different faults.
	DegradedFrom string `json:"degraded_from,omitempty"`
	Reason       string `json:"degraded_reason,omitempty"`
}

// Row is a search result with its text already attached.
type Row struct {
	Source string
	ID     int64
	Text   string
	Date   sql.NullString
}

// Engine searches over a database. Its zero value is not usable: the database is
// required.
type Engine struct {
	DB *store.DB
	// Validate is the gate. Everything this engine runs goes through it, which
	// is the house guarantee: there is no shortcut for the SQL inside.
	Validate func(string) (string, error)
}

// Request is a search by term.
type Request struct {
	// Term is the words joined by "+", as the cascade leaves them.
	Term string
	// SQLLexical is the statement that resolves the search, already compiled by
	// the template. It is handed over ready-made because the one that knows
	// which layers to exclude and which limit to apply is the compiler, not the
	// engine.
	SQLLexical string
	Method     string
	Limit      int
}

// Result is what the search found, and by which route.
type Result struct {
	Rows       []Row
	Provenance Provenance
	// SQL is the lexical statement that actually ran, for the log and so that
	// the operator can repeat it by hand.
	SQL string
}

// Search resolves a request by the requested method, degrading with a reason
// when what is needed is not there.
//
// The degradation ladder always goes towards what does work: FTS without an
// index is the old LIKE. A half-indexed installation answers worse, but it
// answers, and it says so.
func (m *Engine) Search(ctx context.Context, req Request) (Result, error) {
	method := cmp.Or(req.Method, MethodFTS)
	// The provenance names the route that ran, so a method this engine does not
	// know is refused instead of silently answered by the FTS branch under the
	// unknown name.
	if method != MethodFTS && method != MethodLike {
		return Result{}, fmt.Errorf(
			"there is no search method %q: it is %s or %s", method, MethodFTS, MethodLike)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}

	provenance := Provenance{Method: method}

	if provenance.Method == MethodFTS {
		if ok, err := m.hasLexicalIndex(ctx); err != nil {
			return Result{}, err
		} else if !ok {
			provenance.degrade(MethodLike, "the database has no lexical index yet: it needs indexing")
		}
	}

	switch provenance.Method {
	case MethodLike:
		// The LIKE is both the reference and the floor: the usual template
		// compiles it and our caller runs it. Here it is only declared.
		return Result{Provenance: provenance}, nil

	default: // MethodFTS
		rows, err := m.lexical(ctx, req.SQLLexical, limit)
		if err != nil {
			return Result{}, err
		}
		return Result{Rows: rows, Provenance: provenance, SQL: req.SQLLexical}, nil
	}
}

// read is the only way SQL leaves this engine: through the gate and over the
// read-only connection. There is no shortcut for the SQL inside, which is the
// house guarantee, and the verdict names which of the engine's own queries was
// refused.
func (m *Engine) read(ctx context.Context, stmt, what string) (*sql.Rows, error) {
	validated := stmt
	if m.Validate != nil {
		var err error
		if validated, err = m.Validate(stmt); err != nil {
			return nil, fmt.Errorf("the gate rejects %s: %w", what, err)
		}
	}
	reader, err := m.DB.ReadOnly()
	if err != nil {
		return nil, err
	}
	rows, err := reader.QueryContext(ctx, validated)
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", what, err)
	}
	return rows, nil
}

// degrade moves to the method that does work, remembering the first one that
// could not be served: a second fall is still the first one's fault.
func (p *Provenance) degrade(to, reason string) {
	if p.DegradedFrom == "" {
		p.DegradedFrom = p.Method
		p.Reason = reason
	}
	p.Method = to
}

// lexical runs the SQL of the search, which comes in already compiled and goes
// through the gate like any other.
func (m *Engine) lexical(ctx context.Context, stmt string, limit int) ([]Row, error) {
	if strings.TrimSpace(stmt) == "" {
		return nil, nil
	}
	rows, err := m.read(ctx, stmt, "the lexical search")
	if err != nil {
		return nil, err
	}
	// The bm25 rank is selected because a UNION ALL can only order by a column
	// it selects, and it is read into nothing: the order is the answer.
	var priority sql.NullInt64
	var rank sql.NullFloat64
	return collect(rows, limit, &priority, &rank)
}

// collect reads what every search statement returns, which is a row of source,
// identifier, text and date, plus whatever else that statement had to select in
// order to be ordered.
func collect(rows *sql.Rows, limit int, alsoSelected ...any) ([]Row, error) {
	defer rows.Close()
	var out []Row
	for rows.Next() && len(out) < limit {
		var f Row
		var text sql.NullString
		into := append([]any{&f.Source, &f.ID, &text, &f.Date}, alsoSelected...)
		if err := rows.Scan(into...); err != nil {
			return nil, err
		}
		if !text.Valid || strings.TrimSpace(text.String) == "" {
			continue
		}
		f.Text = text.String
		out = append(out, f)
	}
	return out, rows.Err()
}

// hasLexicalIndex is the one question of the degradation ladder. It is answered
// with the index state and not by counting rows: on the real database, counting
// the lexical index means walking all of it.
func (m *Engine) hasLexicalIndex(ctx context.Context) (bool, error) {
	state, err := readState(ctx, m.DB, keyLexical, "check the lexical index")
	return state == "built", err
}
