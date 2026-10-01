//go:build acceptance

package acceptance

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	_ "github.com/thellmwhisperer/la-roca/internal/store/payloadhash"
)

func TestLegacyWatermarkCompatibility(t *testing.T) {
	cases := []struct {
		name, binary string
		published    bool
	}{{name: "branch"}}
	if published := os.Getenv("ROCA_DELTA_PUBLISHED_BIN"); published != "" {
		cases = append(cases, struct {
			name, binary string
			published    bool
		}{"published", published, true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := aWorldIn(t, "legacy-watermark")
			if tc.binary != "" {
				m.binary = tc.binary
			}
			run := func(args ...string) string {
				t.Helper()
				out, code := m.runUnder(t, nil, args...)
				if code != 0 {
					t.Fatalf("CLI %v exit=%d: %s", args, code, out)
				}
				return out
			}
			version := strings.TrimSpace(run("--version"))
			if tc.published && !strings.HasPrefix(version, "roca v1.90.0 ") {
				t.Fatalf("expected pinned v1.90.0 control, got %s", version)
			}
			run("init", "--db-path", filepath.Join(m.home, ".roca", "roca.db"), "--json")
			dir := filepath.Join(m.home, ".claude", "projects", "-synthetic-delta")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
			turn := func(question, answer, stamp string) string {
				return `{"type":"user","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"` + stamp + `","cwd":"/synthetic/delta","message":{"content":"` + question + `"}}` + "\n" + `{"type":"assistant","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"` + stamp + `","message":{"model":"fixture-model","content":[{"type":"text","text":"` + answer + `"}]}}` + "\n"
			}
			body := turn("first question", "first answer", "2026-09-20T10:00:00Z")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			run("ingest", "--json")
			corpus := filepath.Join(m.home, ".roca", "plugins", "roca-corpus", "roca-corpus.db")
			db, err := sql.Open("sqlite", corpus)
			if err != nil {
				t.Fatal(err)
			}
			var fingerprint string
			if err := db.QueryRow(`SELECT fingerprint FROM ingest_file_state WHERE path=?`, path).Scan(&fingerprint); err != nil {
				t.Fatal(err)
			}
			machine := strings.Index(fingerprint, ":machine:")
			parser := strings.Index(fingerprint, ":parser:")
			if machine < 0 || parser < machine {
				t.Fatalf("missing machine-aware fixture watermark")
			}
			legacy := fingerprint[:machine] + fingerprint[parser:]
			if _, err := db.Exec(`UPDATE ingest_file_state SET fingerprint=?,metadata=json_remove(metadata,'$.machine') WHERE path=?`, legacy, path); err != nil {
				t.Fatal(err)
			}
			db.Close()
			before := durableDigests(t, m.home)
			var unchanged ingest.Result
			if err := json.Unmarshal([]byte(run("ingest", "--json")), &unchanged); err != nil {
				t.Fatal(err)
			}
			after := durableDigests(t, m.home)
			if tc.published {
				if unchanged.FilesRead != 1 {
					t.Fatalf("published did not reparse legacy watermark: %+v", unchanged)
				}
			} else if unchanged.FilesRead != 0 || !reflect.DeepEqual(before, after) {
				t.Fatalf("unchanged legacy wrote corpus state or parsed: read=%d identical=%t", unchanged.FilesRead, reflect.DeepEqual(before, after))
			}
			t.Logf("%s: legacy unchanged files_read=%d files_skipped=%d durable_state_unchanged=%t", tc.name, unchanged.FilesRead, unchanged.FilesSkipped, reflect.DeepEqual(before, after))
			// Restore the legacy watermark so the append exercises legacy compatibility too.
			db, err = sql.Open("sqlite", corpus)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE ingest_file_state SET fingerprint=?,metadata=json_remove(metadata,'$.machine') WHERE path=?`, legacy, path); err != nil {
				t.Fatal(err)
			}
			db.Close()
			body += turn("second question", "second answer", "2026-09-20T10:01:00Z")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			var changed ingest.Result
			if err := json.Unmarshal([]byte(run("ingest", "--json")), &changed); err != nil {
				t.Fatal(err)
			}
			if changed.FilesRead != 1 || changed.Delta.Exchanges != 1 {
				t.Fatalf("real append did not land: %+v", changed)
			}
			t.Logf("%s: changed legacy files_read=%d delta_exchanges=%d", tc.name, changed.FilesRead, changed.Delta.Exchanges)
		})
	}
}
