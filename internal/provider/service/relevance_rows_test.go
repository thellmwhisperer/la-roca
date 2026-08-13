package service

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

func TestSearchRowsPreferAnswersOverEchoesAndThinking(t *testing.T) {
	res := QueryResult{Question: "who is Ana"}
	res.foundSearch([]string{"source", "id", "text"}, []map[string]any{
		{"source": "thinking", "id": int64(1), "text": `test for "who is Ana"`},
		{"source": "exchange", "id": int64(2), "text": `run roca query "who is Ana"`},
		{"source": "exchange", "id": int64(3), "text": "Ana led the engineering conversation"},
		{"source": "memory", "id": int64(4), "text": "Ana is Head of Software Engineering"},
		{"source": "memory", "id": int64(5), "text": `documentation quotes "who is Ana"`},
	})

	want := []int64{4, 5, 3, 2, 1}
	for i, id := range want {
		if got := res.Rows[i]["id"]; got != id {
			t.Fatalf("row %d id = %v, want %d; rows=%v", i, got, id, res.Rows)
		}
	}
}

func TestSearchRowsWithNullTextAreRemoved(t *testing.T) {
	res := QueryResult{Question: "resonance"}
	res.foundSearch([]string{"source", "id", "text"}, []map[string]any{
		{"source": "exchange", "id": int64(1), "text": nil},
		{"source": "exchange", "id": int64(2), "text": "health cluster about resonance"},
	})
	if res.RowCount != 1 || res.Rows[0]["id"] != int64(2) {
		t.Fatalf("null text row survived: count=%d rows=%v", res.RowCount, res.Rows)
	}
}

func TestSearchRowsWithIdenticalSourceAndTextAreDeduplicated(t *testing.T) {
	res := QueryResult{Question: "registro duplicado"}
	res.foundSearch([]string{"source", "id", "text"}, []map[string]any{
		{"source": "memory", "id": int64(1), "text": "registro duplicado"},
		{"source": "memory", "id": int64(2), "text": "registro duplicado"},
	})
	if res.RowCount != 1 || res.Rows[0]["id"] != int64(1) {
		t.Fatalf("duplicate rows survived: count=%d rows=%v", res.RowCount, res.Rows)
	}
}

func TestMergedSearchLimitOrdersCoreAndAttachedRowsTogetherBeforeTruncating(t *testing.T) {
	const label = "plugin:roca-ops"
	merged := []map[string]any{
		{"id": int64(1), "created_at": "2026-08-10T12:00:00Z", plugin.ProvenanceColumn: "core"},
		{"id": int64(2), "created_at": "2026-08-12T12:00:00Z", plugin.ProvenanceColumn: "core"},
		{"id": int64(3), "created_at": "2026-08-11T12:00:00Z", plugin.ProvenanceColumn: label},
		{"id": int64(4), "created_at": "2026-08-13T12:00:00Z", plugin.ProvenanceColumn: label},
	}
	for _, one := range []struct {
		name    string
		limit   int
		wantIDs []int64
	}{
		{"a global limit keeps the newest rows from both databases", 3, []int64{4, 2, 3}},
		{"an ample limit still returns every row in recency order", 10, []int64{4, 2, 3, 1}},
	} {
		t.Run(one.name, func(t *testing.T) {
			got := limitMergedSearchRows(merged, one.limit)
			if len(got) != len(one.wantIDs) {
				t.Fatalf("rows = %v, want ids %v", got, one.wantIDs)
			}
			for index, want := range one.wantIDs {
				if got[index]["id"] != want {
					t.Fatalf("row %d id = %v, want %d; rows = %v", index, got[index]["id"], want, got)
				}
			}
		})
	}
}

func TestModelRowsKeepTheSQLOrderAndEveryValue(t *testing.T) {
	rows := []map[string]any{
		{"source": "thinking", "id": int64(1), "text": "first"},
		{"source": "memory", "id": int64(2), "text": ""},
		{"source": "memory", "id": int64(3), "text": "first"},
	}
	res := QueryResult{Question: "preserve model rows"}
	res.found([]string{"source", "id", "text"}, rows)

	if res.RowCount != 3 {
		t.Fatalf("model row count = %d, want 3: %v", res.RowCount, res.Rows)
	}
	for i, want := range []int64{1, 2, 3} {
		if got := res.Rows[i]["id"]; got != want {
			t.Fatalf("model row %d id = %v, want %d: %v", i, got, want, res.Rows)
		}
	}
}
