package service

import (
	"context"

	"github.com/thellmwhisperer/la-roca/internal/query"
	"github.com/thellmwhisperer/la-roca/internal/search"
)

// searchByTerm resolves the term-search template by the best available route,
// and also returns the provenance of that decision.
//
// The LIKE does not go away: it is the floor the degradation ladder falls to
// when the database is not indexed yet, and it is the reference competitor the
// golden bench measures the index against.
func (s *Service) searchByTerm(ctx context.Context, plan query.Plan, method string,
	maxChars int, matchAny bool) (columns []string, rows []map[string]any, stmt string,
	provenance *search.Provenance, err error) {

	gate, err := s.theGate()
	if err != nil {
		return nil, nil, "", nil, err
	}
	engine := &search.Engine{DB: s.db, Validate: gate.Validate}

	limit := plan.Limit
	if limit <= 0 {
		limit = 10
	}

	// The lexical SQL is compiled for the answer's limit: there is no fusion
	// that needs to look beyond the first rows.
	var sqlLexical string
	if method != search.MethodLike {
		if matchAny {
			sqlLexical, err = query.RenderSQLFTSAny(plan, s.registry.SearchExcluded(), limit)
		} else {
			sqlLexical, err = query.RenderSQLFTS(plan, s.registry.SearchExcluded(), limit)
		}
		if err != nil {
			return nil, nil, "", nil, err
		}
	}

	res, err := engine.Search(ctx, search.Request{
		Term:       plan.Term,
		SQLLexical: sqlLexical,
		Method:     method,
		Limit:      limit,
	})
	if err != nil {
		return nil, nil, "", nil, err
	}

	if res.Provenance.Method == search.MethodLike {
		// The floor: the cross-source LIKE, put through the same gate as the
		// model's SQL.
		like, err := query.RenderSQLLike(plan, s.registry.SearchExcluded())
		if err != nil {
			return nil, nil, "", nil, err
		}
		gate, err := s.theGate()
		if err != nil {
			return nil, nil, "", nil, err
		}
		validated, err := gate.Validate(like)
		if err != nil {
			return nil, nil, "", nil, err
		}
		columns, rows, err := s.execute(ctx, validated, plan.Term, maxChars)
		if err != nil {
			return nil, nil, "", nil, err
		}
		return columns, rows, validated, &res.Provenance, nil
	}

	columns = []string{"source", "id", "text", "created_at"}
	rows = make([]map[string]any, 0, len(res.Rows))
	for _, row := range res.Rows {
		// A row with no date reads as null and never as the empty string: the
		// two mean different things to whoever renders the answer.
		var date any
		if row.Date.Valid {
			date = row.Date.String
		}
		rows = append(rows, map[string]any{
			"source":     row.Source,
			"id":         row.ID,
			"text":       truncate(row.Text, maxChars, plan.Term),
			"created_at": date,
		})
	}
	return columns, rows, res.SQL, &res.Provenance, nil
}

// Index leaves the database ready to search. It is idempotent and cheap when
// there is nothing new, which is what allows init to call it without punishing
// whoever already had it indexed.
func (s *Service) Index(ctx context.Context) (search.Report, error) {
	if s.opts.ReadOnly {
		return search.Report{}, errReadOnly
	}
	return search.Index(ctx, s.db)
}
