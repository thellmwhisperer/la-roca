package ingest

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

// A Codex turn names itself in turn_context and task_complete. That source-owned
// turn identity is recorded per exchange, beside the call identities, without
// changing which exchanges the rollout yields.
func TestCodexTurnIdentityIsRecordedPerExchange(t *testing.T) {
	rollout := `{"type":"session_meta","payload":{"id":"turn-identity"}}
{"type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-synthetic"}}
{"type":"event_msg","payload":{"type":"user_message","message":"question"}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","last_agent_message":"answer"}}
`
	records, err := parsers.Parse(parsers.KindCodexSession, []byte(rollout),
		parsers.FileMeta{SessionID: "turn-identity"})
	if err != nil {
		t.Fatal(err)
	}
	db := corpusDatabase(t)
	writeHarvestRecords(t, db, records)
	var turnID string
	if err := db.SQL().QueryRow(`SELECT COALESCE(json_extract(metadata, '$.source_turn_ids."1"'), '')
		FROM sessions WHERE session_id = ?`, "turn-identity").Scan(&turnID); err != nil {
		t.Fatal(err)
	}
	if turnID != "turn-1" {
		t.Fatalf("recorded turn identity = %q, want turn-1", turnID)
	}
}
