//go:build acceptance

package acceptance

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
)

type siblingPlan struct {
	Applied bool `json:"applied"`
	Groups  []struct {
		Stem      string   `json:"stem"`
		IDs       []string `json:"session_ids"`
		Keep      string   `json:"keep"`
		Status    string   `json:"status"`
		Remove    []string `json:"remove"`
		Exchanges int      `json:"exchanges_to_remove"`
	} `json:"groups"`
}

func TestCollapseHistoricalSiblingSessions(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatal(err)
	}
	home, world := initializedDistributionHome(t, "roca-sibling-collapse-", binary)
	corpus := filepath.Join(home, ".roca", "plugins", "roca-corpus", "roca-corpus.db")
	id := func(group, suffix string) string { return strings.Repeat(group, 35) + suffix }
	open := func() *sql.DB {
		t.Helper()
		db, err := bundledplugin.OpenDatabase(corpus, false)
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	execute := func(db *sql.DB, statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatalf("fixture SQL: %v", err)
		}
	}
	db := open()
	add := func(group, suffix string, start, title any, turns int) {
		t.Helper()
		session := id(group, suffix)
		execute(db, `INSERT INTO sessions(session_id, started_at, title, metadata) VALUES (?, ?, ?, json_object('fixture_sibling', ?))`, session, start, title, session)
		for turn := 1; turn <= turns; turn++ {
			execute(db, `INSERT INTO exchanges(session_id, exchange_number, human_text, agent_text)
			  VALUES (?, ?, ?, ?)`, session, turn, "sapphire question", "sapphire answer")
		}
		execute(db, `INSERT INTO thinking_blocks(session_id, exchange_number, full_text) VALUES (?, 1, ?)`, session, session)
		execute(db, `INSERT INTO tool_uses(session_id, exchange_number, tool_name) VALUES (?, 1, ?)`, session, session)
	}
	for _, suffix := range []string{"0", "1", "2"} {
		turns := 0
		if suffix == "1" {
			turns = 1
		}
		add("a", suffix, "2026-01-01", "empty shells", turns)
		add("b", suffix, nil, nil, 2)
		execute(db, `UPDATE exchanges SET human_text = NULL, agent_text = '' WHERE session_id = ? AND exchange_number = 2`, id("b", suffix))
	}
	add("c", "0", "2026-01-01", "different sizes", 1)
	add("c", "1", "2026-01-01", "different sizes", 2)
	add("c", "2", "2026-01-01", "different sizes", 0)
	for _, group := range []string{"d", "e", "f", "g", "h", "j"} {
		add(group, "0", "2026-01-01", group, 1)
		add(group, "1", "2026-01-01", group, 1)
	}
	execute(db, `UPDATE exchanges SET human_text = 'different human' WHERE session_id = ?`, id("d", "1"))
	execute(db, `UPDATE exchanges SET agent_text = 'different agent' WHERE session_id = ?`, id("e", "1"))
	execute(db, `UPDATE exchanges SET exchange_number = 2 WHERE session_id = ?`, id("f", "1"))
	execute(db, `UPDATE sessions SET title = 'different title' WHERE session_id = ?`, id("g", "1"))
	execute(db, `UPDATE sessions SET started_at = '2026-01-02' WHERE session_id = ?`, id("h", "1"))
	execute(db, `UPDATE exchanges SET human_text = NULL WHERE session_id = ?`, id("j", "0"))
	execute(db, `UPDATE exchanges SET human_text = '' WHERE session_id = ?`, id("j", "1"))
	execute(db, `INSERT INTO sessions(session_id, started_at, title, metadata) VALUES ('short0', '2026-01-01', 'short', '{"copy":0}'), ('short1', '2026-01-01', 'short', '{"copy":1}')`)
	execute(db, `INSERT INTO memories(layer, content, origin, source_session) VALUES ('discovery', 'retained reference', 'agent', ?)`, id("b", "1"))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	counts := func() [5]int {
		t.Helper()
		db := open()
		defer db.Close()
		var got [5]int
		if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM sessions), (SELECT COUNT(*) FROM exchanges),
		  (SELECT COUNT(*) FROM thinking_blocks), (SELECT COUNT(*) FROM tool_uses), (SELECT COUNT(*) FROM memories)`).
			Scan(&got[0], &got[1], &got[2], &got[3], &got[4]); err != nil {
			t.Fatal(err)
		}
		return got
	}
	run := func(extraEnv []string, args ...string) distributionRun {
		t.Helper()
		return world.runAtInput(home, binary, "", extraEnv, append([]string{"dedup", "sessions", "--json"}, args...)...)
	}
	readPlan := func(result distributionRun) siblingPlan {
		t.Helper()
		var plan siblingPlan
		if err := json.Unmarshal([]byte(result.stdout), &plan); err != nil || result.code != 0 {
			t.Fatalf("plan: %v: %+v", err, result)
		}
		return plan
	}
	beforeCounts := counts()
	beforeBytes, err := os.ReadFile(corpus)
	if err != nil {
		t.Fatal(err)
	}
	plain := world.runAt(home, binary, "dedup", "sessions")
	if plain.code != 0 || !strings.Contains(plain.stdout, "dry-run") || !strings.Contains(plain.stdout, id("b", "1")) || !strings.Contains(plain.stdout, "different") {
		t.Fatalf("human dry-run plan = %+v", plain)
	}
	plan := readPlan(run(nil)) // Default target and default dry run.
	if plan.Applied || len(plan.Groups) != 7 {
		t.Fatalf("dry-run plan = %+v", plan)
	}
	for i, group := range plan.Groups {
		switch i {
		case 0:
			if group.Keep != id("a", "1") || !reflect.DeepEqual(group.Remove, []string{id("a", "0"), id("a", "2")}) || group.Exchanges != 0 || group.Status != "collapse" {
				t.Fatalf("empty-shell plan = %+v", group)
			}
		case 1:
			if group.Keep != id("b", "0") || !reflect.DeepEqual(group.Remove, []string{id("b", "1"), id("b", "2")}) || group.Exchanges != 4 || group.Status != "collapse" {
				t.Fatalf("identical-copy plan = %+v", group)
			}
		default:
			if group.Status != "different" || len(group.Remove) != 0 || group.Exchanges != 0 {
				t.Fatalf("different-copy plan = %+v", group)
			}
		}
	}
	readPlan(run(nil, corpus, "--read-only"))
	for _, refusal := range []distributionRun{
		run(nil, corpus, "--apply", "--read-only"),
		run([]string{"ROCA_READ_ONLY=1"}, corpus, "--apply"),
	} {
		if refusal.code == 0 || !strings.Contains(refusal.stderr, "read-only") {
			t.Fatalf("read-only apply refusal = %+v", refusal)
		}
	}
	afterBytes, err := os.ReadFile(corpus)
	if err != nil || !bytes.Equal(beforeBytes, afterBytes) || counts() != beforeCounts {
		t.Fatalf("inspection or refusal changed corpus: %v", err)
	}
	// Failure after child deletion must roll back the entire repair.
	db = open()
	execute(db, `CREATE TRIGGER stop_sibling_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'fixture refused deletion'); END`)
	db.Close()
	failed := run(nil, corpus, "--apply")
	if failed.code == 0 || !strings.Contains(failed.stderr, "fixture refused deletion") || counts() != beforeCounts {
		t.Fatalf("failed apply did not roll back: %+v, counts %v", failed, counts())
	}
	db = open()
	var source string
	if err := db.QueryRow(`SELECT source_session FROM memories`).Scan(&source); err != nil || source != id("b", "1") {
		t.Fatalf("failed apply changed memory reference: %q, %v", source, err)
	}
	execute(db, `DROP TRIGGER stop_sibling_delete`)
	db.Close()
	written := readPlan(run(nil, corpus, "--apply", "--json"))
	if !written.Applied || !reflect.DeepEqual(written.Groups, plan.Groups) {
		t.Fatalf("applied plan differs from dry run: %+v", written)
	}
	wantCounts := beforeCounts
	for _, index := range []int{0, 1, 2, 3} {
		wantCounts[index] -= 4
	}
	if got := counts(); got != wantCounts {
		t.Fatalf("applied counts = %v, want %v", got, wantCounts)
	}
	db = open()
	if err := db.QueryRow(`SELECT source_session FROM memories`).Scan(&source); err != nil || source != id("b", "0") {
		t.Fatalf("memory lost its surviving parent: %q, %v", source, err)
	}
	for _, table := range []string{"sessions", "exchanges", "thinking_blocks", "tool_uses"} {
		var removed int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE session_id IN (?, ?, ?, ?)`,
			id("a", "0"), id("a", "2"), id("b", "1"), id("b", "2")).Scan(&removed); err != nil || removed != 0 {
			t.Fatalf("removed copies remain in %s: %d, %v", table, removed, err)
		}
	}
	var indexed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM exchanges_fts WHERE exchanges_fts MATCH 'sapphire'`).Scan(&indexed); err != nil || indexed != wantCounts[1]-1 {
		t.Fatalf("exchange FTS = %d, want %d: %v", indexed, wantCounts[1]-1, err)
	}
	db.Close()
	query := world.runAt(home, binary, "exec", "SELECT COUNT(*) AS n FROM plugin_roca_corpus.exchanges", "--json", "--read-only")
	if query.code != 0 || !strings.Contains(query.stdout, `"n": 18`) {
		t.Fatalf("served exchange count = %+v", query)
	}
	doctor := world.runAt(home, binary, "doctor", "--json", "--read-only")
	var doc map[string]any
	if err := json.Unmarshal([]byte(doctor.stdout), &doc); err != nil || doctor.code != 0 {
		t.Fatalf("doctor after collapse: %v: %+v", err, doctor)
	}
	warnings := duplicateSessionWarnings(doc)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "6 extra rows in 5 groups") {
		t.Fatalf("doctor retained groups other than the listed variants: %q", warnings)
	}
	repeated := readPlan(run(nil, "--apply"))
	if len(repeated.Groups) != 5 || counts() != wantCounts {
		t.Fatalf("repeat apply = %+v, counts %v", repeated, counts())
	}
	for _, group := range repeated.Groups {
		if group.Status != "different" || len(group.Remove) != 0 {
			t.Fatalf("repeat apply removed a variant: %+v", group)
		}
	}
	plain = world.runAt(home, binary, "dedup", "sessions", "--apply")
	if plain.code != 0 || !strings.Contains(plain.stdout, "applied") || counts() != wantCounts {
		t.Fatalf("human apply plan = %+v", plain)
	}
	// Identical all-empty groups keep their lowest session ID.
	db = open()
	add("k", "0", "2026-01-01", "all empty", 0)
	add("k", "1", "2026-01-01", "all empty", 0)
	db.Close()
	emptyCounts := counts()
	withEmpty := readPlan(run(nil, "--apply"))
	last := withEmpty.Groups[len(withEmpty.Groups)-1]
	wantEmptyCounts := emptyCounts
	wantEmptyCounts[0]--
	wantEmptyCounts[2]--
	wantEmptyCounts[3]--
	if last.Keep != id("k", "0") || last.Status != "collapse" || !reflect.DeepEqual(last.Remove, []string{id("k", "1")}) || counts() != wantEmptyCounts {
		t.Fatalf("all-empty group collapse = %+v, counts %v", last, counts())
	}
	for _, args := range [][]string{
		{filepath.Join(home, "missing.db")},
		{filepath.Join(home, "missing.db"), "--apply"},
		{filepath.Join(home, ".roca", "plugins", "roca-ops", "roca-ops.db")},
	} {
		if failed := run(nil, args...); failed.code == 0 {
			t.Fatalf("invalid corpus accepted: %+v", failed)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "missing.db")); !os.IsNotExist(err) {
		t.Fatalf("refusal created a missing corpus: %v", err)
	}
}
