//go:build acceptance

package acceptance

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/thellmwhisperer/la-roca/internal/store/payloadhash"
)

// The published control is opt-in; ordinary regression lives in internal/ingest.
// Both executables act on the same declared lab corpus, with unchanged sources.
func TestCodexIdentityPublished(t *testing.T) {
	published := os.Getenv("ROCA_CODEX_PUBLISHED_BIN")
	if published == "" {
		t.Skip("set ROCA_CODEX_PUBLISHED_BIN to the v1.84.8 control")
	}
	m := aWorldIn(t, "codex-identity")
	branch := m.binary
	m.binary = published
	run := func(args ...string) string {
		t.Helper()
		out, code := m.runUnder(t, nil, args...)
		if code != 0 {
			t.Fatalf("CLI %v: exit=%d %s", args, code, out)
		}
		return out
	}
	version := strings.TrimSpace(run("--version"))
	if !strings.HasPrefix(version, "roca v1.84.8 ") {
		t.Fatalf("wrong published control: %s", version)
	}
	const id = "019aba72-aa57-7d93-a12c-b6e65c0dca6b"
	sources := filepath.Join(m.home, ".codex")
	if err := os.MkdirAll(filepath.Join(sources, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	history, err := os.ReadFile("../../internal/ingest/testdata/codex-stem-history.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(sources, "history.jsonl"):                    string(history),
		filepath.Join(sources, "sessions", "rollout-"+id+".jsonl"): `{"type":"session_meta","payload":{"id":"` + id + `"}}` + "\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "--db-path", filepath.Join(m.home, ".roca", "roca.db"), "--json")
	path := filepath.Join(m.home, ".roca", "plugins", "roca-corpus", "roca-corpus.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scalar := func(query string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if scalar(`SELECT count(*) FROM sessions WHERE session_id='`+id+`'`) != 1 {
		t.Fatal("fresh published ingest changed the parser ID")
	}
	fixture, err := os.ReadFile("../../internal/ingest/testdata/codex-stem-split.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Keep the watermarks; replace only the fresh corpus with the declared
	// inherited shape. This is the masking condition in the reported incident.
	if _, err := db.Exec(`DELETE FROM exchanges; DELETE FROM sessions;` + string(fixture)); err != nil {
		t.Fatal(err)
	}
	digest := func(table string) string {
		t.Helper()
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		for rows.Next() {
			values, dest := make([]any, len(cols)), make([]any, len(cols))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			for i, name := range cols {
				if name == "session_id" || name == "source_session" {
					values[i] = nil
				}
			}
			if err := json.NewEncoder(hash).Encode(values); err != nil {
				t.Fatal(err)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", hash.Sum(nil))
	}
	before := map[string]string{}
	for _, table := range []string{"exchanges", "tool_uses", "thinking_blocks", "memories"} {
		before[table] = digest(table)
	}
	for _, label := range []string{"published", "branch", "branch-repeat"} {
		if label != "published" {
			m.binary = branch
		}
		var result struct {
			Errors      *int   `json:"errors"`
			WriteFailed int    `json:"write_failed"`
			FilesRead   int    `json:"files_read"`
			ElapsedMS   *int64 `json:"elapsed_ms"`
		}
		if err := json.Unmarshal([]byte(run("ingest", "--json")), &result); err != nil {
			t.Fatal(err)
		}
		if result.Errors == nil || *result.Errors != 0 || result.WriteFailed != 0 || result.FilesRead != 0 || result.ElapsedMS == nil {
			t.Fatalf("missing/changed counters: %+v", result)
		}
		wantSessions, wantExact := 2, 1
		if label == "published" {
			wantSessions, wantExact = 3, 0
		}
		if scalar("SELECT count(*) FROM sessions") != wantSessions || scalar(`SELECT count(*) FROM sessions WHERE session_id='`+id+`'`) != wantExact {
			t.Fatalf("%s identity result incorrect", label)
		}
		if scalar(`SELECT count(DISTINCT json_extract(metadata,'$.codex_thread_id')) FROM sessions`) != 2 {
			t.Fatal("logical thread loss")
		}
		for table, want := range before {
			if digest(table) != want {
				t.Fatalf("%s changed %s child payloads", label, table)
			}
		}
		if label != "published" {
			for table, want := range map[string]int{"exchanges": 6, "tool_uses": 48} {
				if scalar("SELECT count(*) FROM "+table+" WHERE session_id='"+id+"'") != want {
					t.Fatal("child ownership not canonical")
				}
			}
		}
		t.Logf("%s: elapsed_ms=%d paired with same 2 logical threads, 6 exchanges, 48 tools (48 session-level, 1 failure), child row IDs/payload digests equal, errors=0 write_failed=0; physical sessions=%d exact_source_session=%d; bytes/read-cost unmeasured", label, *result.ElapsedMS, wantSessions, wantExact)
	}
	t.Logf("published control: %s; branch: %s", version, strings.TrimSpace(run("--version")))
}
