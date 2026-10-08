package ingest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// coworkAuditTurn is one human turn as a Cowork audit writes it: the turn twice,
// as the runtime logs it, then the reply.
func coworkAuditTurn(session, at, question, answer string) string {
	user := `{"type":"user","session_id":"` + session + `","timestamp":"` + at +
		`","_audit_timestamp":"` + at + `","_audit_hmac":"h","message":{"role":"user","content":"` + question + `"}}` + "\n"
	return user + user +
		`{"type":"assistant","session_id":"` + session + `","_audit_timestamp":"` + at +
		`","_audit_hmac":"h","message":{"role":"assistant","content":[{"type":"text","text":"` + answer + `"}]}}` + "\n"
}

// The layout is the one Cowork writes under <acct>/<org>/: the metadata file,
// and beside it the session sandbox holding the transcript, the audit log, and
// the runtime's own JSON.
func TestCoworkSessionLayoutIngestsTheAuditAndExcludesTheSandbox(t *testing.T) {
	world := newWorld(t)
	roots := world.roots()
	org := filepath.Join(roots.CoworkSessions, "acct", "org")
	sandbox := filepath.Join(org, "local_abc")
	world.write(t, filepath.Join(org, "local_abc.json"), `{"cliSessionId":"cli-1","sessionId":"local_abc","title":"audited"}`)
	world.write(t, filepath.Join(sandbox, ".claude", "projects", "-sessions-abc", "cli-1.jsonl"),
		`{"type":"user","sessionId":"cli-1","timestamp":"2026-08-01T12:10:00Z","message":{"role":"user","content":"kept question"}}
{"type":"assistant","sessionId":"cli-1","timestamp":"2026-08-01T12:10:05Z","message":{"role":"assistant","content":[{"type":"text","text":"kept answer"}]}}
`)
	// The first turn was compacted out of the transcript; only the audit has it.
	world.write(t, filepath.Join(sandbox, "audit.jsonl"),
		coworkAuditTurn("cli-1", "2026-08-01T12:00:00Z", "compacted question", "compacted answer")+
			coworkAuditTurn("cli-1", "2026-08-01T12:10:00Z", "kept question", "kept answer"))
	sandboxJSON := []string{
		filepath.Join(sandbox, ".claude.json"),
		filepath.Join(sandbox, ".claude", "settings.json"),
		filepath.Join(sandbox, ".claude", "plugins", "known_marketplaces.json"),
		filepath.Join(sandbox, "work", "package-lock.json"),
	}
	for _, path := range sandboxJSON {
		world.write(t, path, `{"numStartups":1}`)
	}
	// A session that never wrote an audit log keeps today's reading.
	world.write(t, filepath.Join(org, "local_def.json"), `{"cliSessionId":"cli-2","sessionId":"local_def","title":"unaudited"}`)
	world.write(t, filepath.Join(org, "local_def", ".claude.json"), `{"numStartups":1}`)

	db := rocaDatabase(t)
	ctx := context.Background()
	result, err := Run(ctx, db, registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	for _, category := range result.DiscardSummary {
		if strings.Contains(category.Reason, "no supported identity") {
			t.Fatalf("sandbox JSON was read as session metadata: %+v", result.DiscardSummary)
		}
	}
	excluded := map[string]bool{}
	for _, detail := range result.DiscardDetails {
		if detail.ByDesign {
			excluded[detail.Path] = true
		}
	}
	for _, path := range append(sandboxJSON, filepath.Join(org, "local_def", ".claude.json")) {
		if !excluded[path] {
			t.Errorf("%s is not reported as excluded by design: %+v", path, result.DiscardDetails)
		}
	}

	exchanges := func() []string {
		rows, err := db.SQL().Query(`SELECT human_text FROM exchanges
			WHERE session_id = 'cli-1' ORDER BY exchange_number`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var texts []string
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				t.Fatal(err)
			}
			texts = append(texts, text)
		}
		return texts
	}
	want := "compacted question|kept question"
	if got := strings.Join(exchanges(), "|"); got != want {
		t.Fatalf("cli-1 exchanges = %q, want %q", got, want)
	}

	if _, err := Run(ctx, db, registry(t), Options{Roots: roots}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(exchanges(), "|"); got != want {
		t.Fatalf("re-ingest changed cli-1 exchanges to %q", got)
	}
	if got := countRows(t, db.SQL(), "sessions WHERE session_id = 'cli-2' AND title = 'unaudited'"); got != 1 {
		t.Fatalf("the unaudited session did not land from its metadata: %d", got)
	}
}
