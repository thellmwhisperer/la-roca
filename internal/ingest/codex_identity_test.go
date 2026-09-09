package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store/exactdedup"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

func TestCodexStemSplitIdentity(t *testing.T) {
	for _, canonicalPresent := range []bool{false, true} {
		name := "missing source id"
		if canonicalPresent {
			name = "existing source id"
		}
		t.Run(name, func(t *testing.T) {
			db := rocaDatabase(t)
			ctx := context.Background()
			roots := ResolveRoots(Environment{Home: t.TempDir(), GOOS: "darwin"}, Settings{})
			if err := os.MkdirAll(roots.CodexSessions, 0700); err != nil {
				t.Fatal(err)
			}
			const id = "019aba72-aa57-7d93-a12c-b6e65c0dca6b"
			fossil := `{"type":"session_meta","payload":{"id":"` + id + `"}}` + "\n"
			if err := os.WriteFile(filepath.Join(roots.CodexSessions, "rollout-"+id+".jsonl"), []byte(fossil), 0600); err != nil {
				t.Fatal(err)
			}
			history, err := os.ReadFile("testdata/codex-stem-history.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(roots.CodexRoot, "history.jsonl"), history, 0600); err != nil {
				t.Fatal(err)
			}
			if result, err := Run(ctx, db, registry(t), Options{Roots: roots}); err != nil || result.Errors != 0 {
				t.Fatalf("fresh ingest: %+v %v", result, err)
			}
			if countRows(t, db.SQL(), "sessions WHERE session_id='"+id+"'") != 1 || countRows(t, db.SQL(), "exchanges") != 6 {
				t.Fatal("fresh parser/writer did not preserve source identity")
			}
			// Keep the clean fingerprints and cursors, but replace the served rows
			// with the inherited shape. Unchanged sources must still be repaired.
			exec(t, db.SQL(), `DELETE FROM exchanges; DELETE FROM sessions`)
			fixture, err := os.ReadFile("testdata/codex-stem-split.sql")
			if err != nil {
				t.Fatal(err)
			}
			exec(t, db.SQL(), string(fixture))
			if canonicalPresent {
				exec(t, db.SQL(), `INSERT INTO sessions(session_id,source_agent,metadata) VALUES
				('019aba72-aa57-7d93-a12c-b6e65c0dca6b','codex','{"codex_thread_id":"019aba72-aa57-7d93-a12c-b6e65c0dca6b","codex_rollout_path":"/synthetic/rollout.jsonl"}')`)
				// A third envelope equals the intermediate union of the first two.
				// It must be retired before that union hits the exact-payload guard.
				exec(t, db.SQL(), `INSERT INTO sessions(session_id,source_agent,project,metadata)
				 SELECT '019aba72-aa57-7d93-a12c-b6e65c0dca62',source_agent,project,metadata
				 FROM sessions WHERE session_id='019aba72-aa57-7d93-a12c-b6e65c0dca61';
				 UPDATE sessions SET project=NULL WHERE session_id='019aba72-aa57-7d93-a12c-b6e65c0dca61'`)
			}
			if err := exactdedup.EnsureGuards(ctx, db.SQL()); err != nil {
				t.Fatal(err)
			}
			before := countRows(t, db.SQL(), "sessions")
			if _, err := Run(ctx, db, registry(t), Options{Roots: roots, DryRun: true}); err != nil {
				t.Fatal(err)
			}
			if countRows(t, db.SQL(), "sessions") != before {
				t.Fatal("dry-run changed identity")
			}
			for pass := 0; pass < 4; pass++ {
				if pass == 2 {
					exec(t, db.SQL(), `UPDATE ingest_file_state SET fingerprint='outdated-reading'`)
				}
				result, err := Run(ctx, db, registry(t), Options{Roots: roots})
				if err != nil || result.Errors != 0 {
					t.Fatalf("ingest: %+v %v", result, err)
				}
				if pass == 2 && result.FilesRead == 0 {
					t.Fatal("invalidated fingerprint did not reread the fossil")
				}
				if pass != 2 && result.FilesRead != 0 {
					t.Fatal("repair reread unchanged history or rollout")
				}
				for query, want := range map[string]int{
					"sessions": 2,
					"sessions WHERE session_id='019aba72-aa57-7d93-a12c-b6e65c0dca6b'":                              1,
					"exchanges WHERE session_id='019aba72-aa57-7d93-a12c-b6e65c0dca6b'":                             6,
					"tool_uses WHERE session_id='019aba72-aa57-7d93-a12c-b6e65c0dca6b'":                             48,
					"tool_uses WHERE session_id='019aba72-aa57-7d93-a12c-b6e65c0dca6b' AND exchange_number IS NULL": 48,
					"tool_uses WHERE had_error=1 AND error_message='synthetic failure'":                             1,
					"sessions WHERE session_id='synthetic-correct-thread'":                                          1,
				} {
					if got := countRows(t, db.SQL(), query); got != want {
						t.Errorf("%s = %d, want %d", query, got, want)
					}
				}
			}
		})
	}
}

func TestCodexIdentityReservesChildNumbersOnLaterWrites(t *testing.T) {
	for _, sourceIDs := range []bool{false, true} {
		t.Run(fmt.Sprint("source IDs=", sourceIDs), func(t *testing.T) {
			db := rocaDatabase(t)
			ctx := t.Context()
			fixture, err := os.ReadFile("testdata/codex-stem-split.sql")
			if err != nil {
				t.Fatal(err)
			}
			exec(t, db.SQL(), string(fixture))
			exec(t, db.SQL(), `INSERT INTO tool_uses(session_id,exchange_number,tool_name,had_error,error_message)
			 VALUES('019aba72-aa57-7d93-a12c-b6e65c0dca60',7,'numbered synthetic call',1,'numbered failure');
			 INSERT INTO thinking_blocks(session_id,exchange_number,full_text)
			 VALUES('019aba72-aa57-7d93-a12c-b6e65c0dca60',8,'unmatched reasoning')`)
			if err := reconcileCodexSessionIDs(ctx, db); err != nil {
				t.Fatal(err)
			}
			session := parsers.Session{
				ID: "019aba72-aa57-7d93-a12c-b6e65c0dca6b", SourceAgent: "codex",
				HistoryFallback: true,
				Exchanges: []parsers.Exchange{
					{Number: 7, HumanText: "Later prompt 7"},
					{Number: 8, HumanText: "Later prompt 8"},
				},
			}
			if sourceIDs {
				for i := range session.Exchanges {
					session.Exchanges[i].SourceID = fmt.Sprint("later-prompt-", i)
				}
			}
			for pass := 0; pass < 2; pass++ {
				if err := db.Write(ctx, func(tx *sql.Tx) error {
					_, err := WriteSessions(ctx, tx, []parsers.Session{session})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				for query, want := range map[string]int{
					"sessions": 2, "exchanges": 8, "tool_uses": 49, "thinking_blocks": 1,
					"exchanges WHERE exchange_number IN (7,8)":                                               0,
					"exchanges WHERE exchange_number=9 AND human_text='Later prompt 7'":                      1,
					"exchanges WHERE exchange_number=10 AND human_text='Later prompt 8'":                     1,
					"tool_uses WHERE exchange_number=7 AND had_error=1 AND error_message='numbered failure'": 1,
					"thinking_blocks WHERE exchange_number=8 AND full_text='unmatched reasoning'":            1,
				} {
					if got := countRows(t, db.SQL(), query); got != want {
						t.Errorf("%s=%d want %d", query, got, want)
					}
				}
			}
		})
	}
}

func TestCodexIdentityReferencesAndConflict(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint("conflict=", conflict), func(t *testing.T) {
			db := rocaDatabase(t)
			fixture, err := os.ReadFile("testdata/codex-stem-split.sql")
			if err != nil {
				t.Fatal(err)
			}
			exec(t, db.SQL(), string(fixture))
			// The orphan-only sibling also owns an unmatched numbered call. Its
			// number must never attach it to the other sibling's history prompt.
			exec(t, db.SQL(), `INSERT INTO tool_uses(session_id,exchange_number,tool_name)
			 VALUES('019aba72-aa57-7d93-a12c-b6e65c0dca60',1,'numbered synthetic call');
			 INSERT INTO thinking_blocks(session_id,exchange_number,full_text)
			 VALUES('019aba72-aa57-7d93-a12c-b6e65c0dca61',1,'synthetic reasoning');
			 INSERT INTO memories(source_session,layer,content,origin)
			 VALUES('019aba72-aa57-7d93-a12c-b6e65c0dca60','pattern','synthetic memory','agent');`)
			if conflict {
				exec(t, db.SQL(), `UPDATE sessions SET metadata=json_set(metadata,
				 '$.codex_rollout_path','/synthetic/different-rollout.jsonl')
				 WHERE session_id='019aba72-aa57-7d93-a12c-b6e65c0dca60'`)
			}
			if err := exactdedup.EnsureGuards(context.Background(), db.SQL()); err != nil {
				t.Fatal(err)
			}
			err = reconcileCodexSessionIDs(context.Background(), db)
			if conflict {
				if err == nil || !strings.Contains(err.Error(), "source identity conflicts") {
					t.Fatalf("expected explicit conflict: %v", err)
				}
				if countRows(t, db.SQL(), "sessions") != 3 || countRows(t, db.SQL(), "tool_uses") != 49 || countRows(t, db.SQL(), "exchanges") != 6 {
					t.Fatal("conflict failed to roll back")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for query, want := range map[string]int{
				"exchanges": 6, "tool_uses": 49, "thinking_blocks": 1, "memories": 1,
				"exchanges WHERE exchange_number=7 AND human_text='Synthetic prompt 1'":                             1,
				"thinking_blocks WHERE exchange_number=7":                                                           1,
				"memories WHERE source_session='019aba72-aa57-7d93-a12c-b6e65c0dca6b'":                              1,
				"sessions WHERE json_extract(metadata,'$.source_exchange_ids.synthetic-history.exchange_number')=7": 1,
				"tool_uses WHERE exchange_number=1 AND tool_name='numbered synthetic call'":                         1,
			} {
				if got := countRows(t, db.SQL(), query); got != want {
					t.Errorf("%s=%d want %d", query, got, want)
				}
			}
		})
	}
}
