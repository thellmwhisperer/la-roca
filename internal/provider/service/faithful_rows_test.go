package service_test

import (
	"context"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// The model path returns what the SQL returned.
//
// A query is the question going to the model, the model's SQL going to the
// database and ITS rows coming back. The answer prints that SQL beside the rows,
// so a presentation layer that deduplicates, reorders or drops rows makes the
// printed SQL stop describing what it is printed next to. From the v1 model-only
// decision: no silent filters, and an honest zero.
//
// The cross join asks for each of the three seeded memories three times, so the
// statement's own result set is nine rows of which LIMIT keeps eight, every id
// repeated and ordered by id. Collapsing that to three distinct rows is the
// silent filter this pins shut.
func TestTheModelPathReturnsTheRowsItsSQLProduced(t *testing.T) {
	svc := serviceWithModel(t, answering("codex",
		`SELECT 'memory' AS source, a.id AS id, a.content AS text
		 FROM memories a JOIN memories b ORDER BY a.id LIMIT 8`))

	res, err := svc.Query(context.Background(), service.QueryRequest{Question: theFreeQuestion})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	if res.RowCount != 8 {
		t.Fatalf("row count = %d, want the 8 rows the statement produced: %v",
			res.RowCount, res.Rows)
	}
	seen := map[any]int{}
	previous := int64(-1)
	for i, row := range res.Rows {
		id, ok := row["id"].(int64)
		if !ok {
			t.Fatalf("row %d carries no id: %v", i, row)
		}
		if id < previous {
			t.Fatalf("row %d has id %d after %d: ORDER BY a.id was overridden",
				i, id, previous)
		}
		previous = id
		seen[id]++
	}
	for id, times := range seen {
		if times < 2 {
			t.Errorf("id %v appears %d time: the duplicate rows were collapsed", id, times)
		}
	}
}
