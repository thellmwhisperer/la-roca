package parsers

import (
	"strings"
	"testing"
)

const desktopMetadata = `{
  "cliSessionId": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
  "sessionId": "local-77",
  "cwd": "/w/demo",
  "title": "  fix the ingest  ",
  "createdAt": 1785542400000,
  "lastActivityAt": 1785542520000,
  "model": "test-model",
  "permissionMode": "acceptEdits",
  "initialMessage": "start with the matrix",
  "userSelectedFolders": ["/w/demo"],
  "enabledMcpTools": []
}`

func TestSessionMetadataIsASnapshotWithNoExchange(t *testing.T) {
	records, err := Parse(KindSessionMetadata, []byte(desktopMetadata), FileMeta{
		SourceAgent: "claude-code",
		Project:     "demo",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	session := records.Sessions[0]
	// The CLI's session id is the identity: it is the one the transcript under
	// ~/.claude/projects also carries, which is what makes the two halves meet.
	if session.ID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("session id = %q", session.ID)
	}
	if !session.Snapshot {
		t.Error("a metadata file is a snapshot: it merges over what the transcript wrote")
	}
	if len(session.Exchanges) != 0 {
		t.Errorf("exchanges = %d, want none", len(session.Exchanges))
	}
	if session.Title != "fix the ingest" {
		t.Errorf("title = %q, want it trimmed", session.Title)
	}
	if session.StartedAt != "2026-08-01T00:00:00Z" {
		t.Errorf("started at = %q", session.StartedAt)
	}
	if session.EndedAt != "2026-08-01T00:02:00Z" {
		t.Errorf("ended at = %q", session.EndedAt)
	}
	if session.Metadata["entrypoint"] != "claude-desktop" {
		t.Errorf("entrypoint = %v", session.Metadata["entrypoint"])
	}
	if session.Metadata["local_session_id"] != "local-77" {
		t.Errorf("local session id = %v", session.Metadata["local_session_id"])
	}
	// An empty list is data the runtime declared, not an absence.
	if _, ok := session.Metadata["enabled_mcp_tools"]; !ok {
		t.Errorf("metadata = %+v", session.Metadata)
	}
}

// The legacy alias names the entrypoint, never the source agent. The scanner
// emits `cowork`, which is the name on the supported roster, so a session that
// kept `claude-cowork` as its own source agent would file the same source under
// two identities: one for query grouping and one in the rows.
func TestCoworkMetadataDeclaresItsOwnEntrypoint(t *testing.T) {
	for _, declared := range []string{"cowork", "claude-cowork"} {
		records, err := Parse(KindSessionMetadata, []byte(desktopMetadata),
			FileMeta{SourceAgent: declared})
		if err != nil {
			t.Fatalf("parse %q: %v", declared, err)
		}
		session := records.Sessions[0]
		if got := session.Metadata["entrypoint"]; got != "claude-cowork" {
			t.Errorf("%s: entrypoint = %v, want claude-cowork", declared, got)
		}
		if session.SourceAgent != "cowork" {
			t.Errorf("%s: source agent = %q, want the roster name cowork",
				declared, session.SourceAgent)
		}
	}
}

func TestSessionMetadataWithoutAnIdIsSkipped(t *testing.T) {
	for _, content := range []string{`{"cwd":"/w/demo"}`, `not json at all`, `[]`} {
		records, err := Parse(KindSessionMetadata, []byte(content), FileMeta{})
		if err != nil {
			t.Fatalf("parse %q: %v", content, err)
		}
		if len(records.Sessions) != 0 {
			t.Errorf("%q was not skipped: %+v", content, records)
		}
		if len(records.Discards) != 1 {
			t.Errorf("%q discards = %d, want 1", content, len(records.Discards))
		}
	}
	records, _ := Parse(KindSessionMetadata, []byte(`{"cwd":"/w/demo"}`), FileMeta{})
	if !strings.Contains(records.Discards[0].Reason, "cliSessionId or sessionId") {
		t.Fatalf("missing identity discard = %q", records.Discards[0].Reason)
	}
}

func TestZeroEpochIsReportedAsMissing(t *testing.T) {
	records, err := Parse(KindSessionMetadata,
		[]byte(`{"cliSessionId":"session-1","createdAt":0,"lastActivityAt":0}`), FileMeta{})
	if err != nil {
		t.Fatal(err)
	}
	session := records.Sessions[0]
	if session.StartedAt != "" || session.EndedAt != "" {
		t.Fatalf("zero timestamps became %q to %q", session.StartedAt, session.EndedAt)
	}
	if got := ISOFromEpochSeconds(0); got != "" {
		t.Fatalf("zero seconds became %q", got)
	}
	if got := validInstant("2026-08-01T10:00:00"); got != "" {
		t.Fatalf("zone-less timestamp became %q", got)
	}
	if got := validInstant("2026-08-01T10:00:00Z"); got != "2026-08-01T10:00:00Z" {
		t.Fatalf("RFC3339 timestamp became %q", got)
	}
}

const coworkAudit = `
{"type":"user","session_id":"cw-1","_audit_timestamp":"2026-08-01T11:00:00Z","message":{"content":[{"type":"text","text":"review the report"}]}}
{"type":"assistant","_audit_timestamp":"2026-08-01T11:00:03Z","message":{"content":"reviewed"}}
{"type":"user","_audit_timestamp":"2026-08-01T11:00:04Z","message":{"content":[{"type":"tool_result","tool_use_id":"x","is_error":false}]}}
{"type":"user","_audit_timestamp":"2026-08-01T11:00:10Z","message":{"content":[{"type":"text","text":"and the second one"}]}}
{"type":"assistant","_audit_timestamp":"2026-08-01T11:00:11Z","message":{"content":[{"type":"text","text":"also"}]}}
`

func TestCoworkAuditPairsTurnsAndTakesItsIdentityFromTheSidecar(t *testing.T) {
	records, err := Parse(KindCoworkAudit, []byte(coworkAudit), FileMeta{
		SourceAgent: "claude-cowork",
		Sidecar:     []byte(desktopMetadata),
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	session := records.Sessions[0]
	if session.ID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("session id = %q: the sidecar declares it", session.ID)
	}
	if session.Title != "fix the ingest" {
		t.Errorf("title = %q", session.Title)
	}
	// The tool-result-only line is not a turn: two turns, two exchanges.
	if len(session.Exchanges) != 2 {
		t.Fatalf("exchanges = %d, want 2", len(session.Exchanges))
	}
	if session.Exchanges[0].AgentText != "reviewed" {
		t.Errorf("a bare string answer was lost: %+v", session.Exchanges[0])
	}
	if session.Exchanges[1].HumanText != "and the second one" {
		t.Errorf("second exchange = %+v", session.Exchanges[1])
	}
}

func TestCoworkAuditWithoutASidecarFallsBackToItsOwnSessionID(t *testing.T) {
	records, err := Parse(KindCoworkAudit, []byte(coworkAudit), FileMeta{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := records.Sessions[0].ID; got != "cw-1" {
		t.Errorf("session id = %q, want cw-1", got)
	}
}
