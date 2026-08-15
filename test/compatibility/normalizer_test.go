package compatibility

import (
	"strings"
	"testing"
)

func TestNormalizeJSONKeepsBehaviorAndMasksOnlyRunNoise(t *testing.T) {
	raw := []byte(`{
  "id": 17,
  "source": "memory",
  "database": "core",
  "rank": -1.25,
  "warnings": ["synthetic warning"],
  "created_at": "2026-08-15T12:34:56Z",
  "latency_ms": 9,
  "database_path": "/private/sandbox/.roca/roca.db",
  "correlation_id": "qf_0123456789abcdef"
}`)

	got, err := (Normalizer{Home: "/private/sandbox"}).JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{
		`"id": 17`, `"source": "memory"`, `"database": "core"`,
		`"rank": -1.25`, `"synthetic warning"`,
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("normalized JSON lost %s:\n%s", kept, got)
		}
	}
	for _, masked := range []string{
		`"created_at": "<timestamp>"`, `"latency_ms": "<duration_ms>"`,
		`"database_path": "<home>/.roca/roca.db"`,
		`"correlation_id": "<correlation_id>"`,
	} {
		if !strings.Contains(got, masked) {
			t.Errorf("normalized JSON lacks %s:\n%s", masked, got)
		}
	}
}

func TestNormalizeTextKeepsTOONShapeAndMasksOnlyRunNoise(t *testing.T) {
	raw := "SQL · provider synthetic · model oracle · 17 ms\n" +
		"rows[1]{source,id,database,rank}:\n  memory,17,core,-1.25\n" +
		"database: /private/sandbox/.roca/roca.db\n" +
		"correlation_id: qf_0123456789abcdef\n"

	got := (Normalizer{Home: "/private/sandbox"}).Text(raw)
	for _, kept := range []string{
		"rows[1]{source,id,database,rank}", "memory,17,core,-1.25",
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("normalized text lost %q:\n%s", kept, got)
		}
	}
	for _, removed := range []string{"17 ms", "/private/sandbox", "qf_0123456789abcdef"} {
		if strings.Contains(got, removed) {
			t.Errorf("normalized text kept run noise %q:\n%s", removed, got)
		}
	}
}

func TestNormalizeTextKeepsAnEmptyStreamEmpty(t *testing.T) {
	if got := (Normalizer{}).Text(""); got != "" {
		t.Fatalf("empty stream normalized to %q", got)
	}
}
