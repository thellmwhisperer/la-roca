package service_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestExecRefusesAnUnqualifiedTableSharedByAttachedPlugins(t *testing.T) {
	paths, plugins := scopedBundledPlugins(t)
	svc := initialized(t, paths, func(options *service.Options) {
		options.PluginDir = plugins
		options.RocaOpsEnabled = true
		options.CorpusEnabled = true
	})
	if _, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "discovery", Content: "synthetic proceso carwow marker",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Exec(t.Context(), service.ExecRequest{
		SQL: `SELECT content FROM memories WHERE content LIKE '%proceso carwow%'`,
	})
	if err == nil {
		t.Fatal("unqualified memories returned rows")
	}
	if got := logfile.ErrorType(err); got != service.DegradedInvalidSQL {
		t.Fatalf("error_type = %q, want %q (%v)", got, service.DegradedInvalidSQL, err)
	}
	got := err.Error()
	if !strings.Contains(got, `unqualified table "memories"`) ||
		!strings.Contains(got, "plugin_roca_ops.memories") ||
		!strings.Contains(got, "plugin_roca_corpus.memories") {
		t.Fatalf("exec error = %q", got)
	}

	for _, query := range []string{
		`SELECT content FROM (WITH hits AS (SELECT content FROM memories) SELECT content FROM hits)`,
		`SELECT content FROM plugin_roca_ops.memories o ORDER BY (SELECT MAX(created_at) FROM memories WHERE content = o.content)`,
		`WITH hits AS (WITH memories AS (SELECT 'x' AS content) SELECT content FROM memories) SELECT m.content FROM hits h JOIN memories m ON m.content = h.content`,
		`SELECT COUNT(*) FROM memories m CROSS JOIN plugin_roca_corpus.exchanges e`,
		`SELECT session_id FROM sessions`,
	} {
		_, err := svc.Exec(t.Context(), service.ExecRequest{SQL: query, Timeout: time.Nanosecond, TimeoutSet: true})
		if err == nil || logfile.ErrorType(err) != service.DegradedInvalidSQL || !strings.Contains(err.Error(), "unqualified table") || !strings.Contains(err.Error(), "plugin_roca_corpus.") {
			t.Fatalf("qualification must precede execution for %q: %v", query, err)
		}
	}

	result, err := svc.Exec(t.Context(), service.ExecRequest{
		SQL: `SELECT content FROM plugin_roca_ops.memories WHERE content LIKE '%proceso carwow%'`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 || !strings.Contains(fmt.Sprint(result.Rows[0]["content"]), "proceso carwow") {
		t.Fatalf("qualified result = %+v", result)
	}

	if _, err := svc.Exec(t.Context(), service.ExecRequest{SQL: `SELECT 1 AS n`}); err != nil {
		t.Fatalf("expression SELECT = %v", err)
	}
}
