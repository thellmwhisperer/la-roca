package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteSourceRootsIngestByMachine(t *testing.T) {
	hubHome := t.TempDir()
	workspace := filepath.Join(hubHome, "w")
	cwd := filepath.Join(workspace, "demo")
	hubEnv := Environment{GOOS: "darwin", Home: hubHome, Hostname: "hub"}
	hubSettings := Settings{WorkspaceRoots: []string{workspace}}
	hubRoots := ResolveRoots(hubEnv, hubSettings)
	writeClaudeSession(t, hubRoots, cwd, cwdFixtureSessionID)

	mirror := t.TempDir()
	remoteEnv := Environment{GOOS: "darwin", Home: mirror}
	remoteRoots := ResolveRoots(remoteEnv, Settings{})
	writeClaudeSession(t, remoteRoots, cwd, cwdFixtureSessionID)

	combined := ResolveRoots(hubEnv, Settings{
		WorkspaceRoots: []string{workspace},
		RemoteSources:  []RemoteSource{{Machine: "mini", Root: mirror}},
	})
	if combined.Machine != "hub" || len(combined.Remotes) != 1 || combined.Remotes[0].Machine != "mini" {
		t.Fatalf("roots machine=%q remotes=%+v", combined.Machine, combined.Remotes)
	}

	dry, err := Run(context.Background(), rocaDatabase(t), registry(t),
		Options{Roots: combined, DryRun: true})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	byMachine := map[string]RootScan{}
	for _, scan := range dry.RootScans {
		byMachine[scan.Machine] = scan
	}
	if byMachine["hub"].FilesSeen == 0 || byMachine["mini"].FilesSeen == 0 {
		t.Fatalf("dry-run counts by root = %+v", dry.RootScans)
	}

	db, result := runIngest(t, combined)
	if result.Errors != 0 {
		t.Fatalf("errors = %d: %+v", result.Errors, result.ErrorDetails)
	}

	counts := queryColumn(t, db.SQL(),
		`SELECT machine || '=' || COUNT(*) FROM sessions GROUP BY machine ORDER BY 1`)
	if got := strings.Join(counts, " "); got != "hub=1 mini=1" {
		t.Fatalf("machine counts = %v", counts)
	}

	var localID, remoteID string
	if err := db.SQL().QueryRow(`SELECT session_id FROM sessions WHERE machine = 'hub'`).
		Scan(&localID); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT session_id FROM sessions WHERE machine = 'mini'`).
		Scan(&remoteID); err != nil {
		t.Fatal(err)
	}
	if localID != cwdFixtureSessionID {
		t.Fatalf("local session_id = %q", localID)
	}
	if remoteID != "mini/"+cwdFixtureSessionID {
		t.Fatalf("remote session_id = %q", remoteID)
	}
	exchangeMachines := queryColumn(t, db.SQL(),
		`SELECT DISTINCT COALESCE(machine, '') FROM exchanges ORDER BY 1`)
	if strings.Join(exchangeMachines, " ") != "hub mini" {
		t.Fatalf("exchange machines = %v", exchangeMachines)
	}

	writeClaudeSession(t, remoteRoots, cwd, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	second := runIngestOn(t, db, ResolveRoots(hubEnv, hubSettings))
	if second.Errors != 0 {
		t.Fatalf("second ingest errors = %d: %+v", second.Errors, second.ErrorDetails)
	}
	after := queryColumn(t, db.SQL(),
		`SELECT machine || '=' || COUNT(*) FROM sessions GROUP BY machine ORDER BY 1`)
	if strings.Join(after, " ") != "hub=1 mini=1" {
		t.Fatalf("after removing the remote entry, machine counts = %v", after)
	}
	var later int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM sessions WHERE session_id LIKE '%aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'`).
		Scan(&later); err != nil {
		t.Fatal(err)
	}
	if later != 0 {
		t.Fatal("a file added after the remote entry was removed was still ingested")
	}
}

func TestQualifySessionIDAndMirrorStale(t *testing.T) {
	if got := QualifySessionID("mini", "abc"); got != "mini/abc" {
		t.Fatalf("qualify = %q", got)
	}
	if got := QualifySessionID("mini", "mini/abc"); got != "mini/abc" {
		t.Fatalf("already qualified = %q", got)
	}
	if got := QualifySessionID("", "abc"); got != "abc" {
		t.Fatalf("local id changed: %q", got)
	}

	root := t.TempDir()
	path := filepath.Join(root, ".claude", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if !MirrorStale(root, 24, time.Now()) {
		t.Fatal("a 48h-old mirror was not stale at a 24h threshold")
	}
	if MirrorStale(root, 72, time.Now()) {
		t.Fatal("a 48h-old mirror was stale at a 72h threshold")
	}
	if !MirrorStale(filepath.Join(root, "missing"), 24, time.Now()) {
		t.Fatal("a missing mirror was not stale")
	}
}

func writeClaudeSession(t *testing.T, roots Roots, cwd, sessionID string) {
	t.Helper()
	path := filepath.Join(roots.ClaudeProjects, encodeRoot(cwd), sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","timestamp":"2026-08-01T10:00:00Z","cwd":"` + cwd + `","message":{"content":"question"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"text","text":"answer"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
