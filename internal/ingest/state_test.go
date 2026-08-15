package ingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

func TestFingerprintDetectsSameSizeSameMtimeEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bravo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatalf("fingerprint stayed %q after same-size, same-mtime edit", after)
	}
}

func TestCodexFingerprintCarriesTheHistoryParserRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := targetFingerprint(Target{Path: path, Kind: parsers.KindCodexHistory})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(fingerprint, ":parser:codex-history-v2") {
		t.Fatalf("Codex fingerprint = %q, want the v2 history parser revision", fingerprint)
	}
}

// TestContributedParserVersionRidesInTheFingerprint pins the re-read escape
// hatch a contributed parser has: the reading it declares in its own registry
// line is what the watermark carries, so a build that learned to read more of
// that source stops trusting the fingerprint the poorer reading earned.
func TestContributedParserVersionRidesInTheFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.source")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := Target{Path: path, Kind: parsers.Kind("nova")}
	unversioned, err := targetFingerprint(target)
	if err != nil {
		t.Fatal(err)
	}

	registeredParser = func(name string) (parsers.Registration, bool) {
		return parsers.Registration{Name: name, Version: "nova-v2"}, name == "nova"
	}
	t.Cleanup(func() { registeredParser = parsers.Lookup })

	versioned, err := targetFingerprint(target)
	if err != nil {
		t.Fatal(err)
	}
	if versioned == unversioned || !strings.HasSuffix(versioned, ":parser:nova-v2") {
		t.Fatalf("contributed fingerprint = %q, want the declared reading past %q",
			versioned, unversioned)
	}
}

func TestKnownFingerprintCanMatchMetadataWhenContentCannotBeRead(t *testing.T) {
	state := map[string]FileState{"session": {Fingerprint: "5:10:digest"}}
	if !unchangedMetadata(state, "session", "5:10") {
		t.Fatal("known content fingerprint did not retain its metadata identity")
	}
	if unchangedMetadata(state, "session", "5:11") {
		t.Fatal("changed metadata was accepted")
	}
}

func TestExportConversationFingerprintIncludesParserRevision(t *testing.T) {
	for _, kind := range []parsers.Kind{
		parsers.KindClaudeWebConversations,
		parsers.KindChatGPTWebConversations,
	} {
		t.Run(string(kind), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "conversations.json")
			if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
				t.Fatal(err)
			}
			legacy, err := Fingerprint(path)
			if err != nil {
				t.Fatal(err)
			}
			current, err := targetFingerprint(Target{Path: path, Kind: kind})
			if err != nil {
				t.Fatal(err)
			}
			if current == legacy {
				t.Fatalf("parser revision did not change legacy fingerprint %q", legacy)
			}
			metadata, err := metadataFingerprint(path)
			if err != nil {
				t.Fatal(err)
			}
			if !unchangedMetadata(map[string]FileState{"export": {Fingerprint: current}}, "export", metadata) {
				t.Fatal("parser-aware fingerprint lost its metadata prefix")
			}
		})
	}
}

func TestDatabaseFingerprintChangesWithWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
		CREATE TABLE events (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	target := Target{Path: path, Kind: parsers.KindOpenCodeDB}
	before, err := targetFingerprint(target)
	if err != nil {
		t.Fatal(err)
	}
	mainBefore, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events VALUES ('committed in the wal')`); err != nil {
		t.Fatal(err)
	}
	mainAfter, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if mainAfter != mainBefore {
		t.Fatalf("test setup checkpointed the database: %q != %q", mainAfter, mainBefore)
	}
	after, err := targetFingerprint(target)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatalf("database fingerprint stayed %q after a WAL commit", after)
	}
}
