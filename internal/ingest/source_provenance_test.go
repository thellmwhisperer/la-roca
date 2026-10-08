package ingest

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

// Two calls with the same tool, parameters and verdict are still two calls. The
// row each one landed on is recorded by the call id Codex wrote, never by
// comparing payloads.
func TestIdenticalCodexCallsKeepTheirRecordedSourceIdentity(t *testing.T) {
	prefix := `{"type":"session_meta","payload":{"id":"identical-identity"}}
{"type":"event_msg","payload":{"type":"user_message","message":"first"}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"one"}}
{"type":"event_msg","payload":{"type":"user_message","message":"second"}}
{"type":"response_item","payload":{"type":"function_call","call_id":"x","name":"shell","arguments":"{\"cmd\":\"inspect\"}"}}
`
	tail := `{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"two"}}
{"type":"event_msg","payload":{"type":"user_message","message":"third"}}
{"type":"response_item","payload":{"type":"function_call","call_id":"y","name":"shell","arguments":"{\"cmd\":\"inspect\"}"}}
`
	db := corpusDatabase(t)
	for _, content := range []string{prefix, prefix + tail} {
		records, err := parsers.Parse(parsers.KindCodexSession, []byte(content),
			parsers.FileMeta{SessionID: "identical-identity"})
		if err != nil {
			t.Fatal(err)
		}
		writeHarvestRecords(t, db, records)
	}
	rows, err := db.SQL().Query(`SELECT t.exchange_number,
		COALESCE(json_extract(s.metadata, '$.source_tool_ids."' || t.id || '"'), '')
		FROM tool_uses t JOIN sessions s ON s.session_id = t.session_id
		WHERE t.session_id = ? ORDER BY t.id`, "identical-identity")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]sql.NullInt64{}
	for rows.Next() {
		var number sql.NullInt64
		var callID string
		if err := rows.Scan(&number, &callID); err != nil {
			t.Fatal(err)
		}
		if _, twice := got[callID]; twice || callID == "" {
			t.Fatalf("call identity %q recorded for more than one row or none", callID)
		}
		got[callID] = number
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["x"].Int64 != 2 || got["y"].Valid {
		t.Fatalf("recorded call identities = %+v, want x on exchange 2 and y at session level", got)
	}
	var recorded int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM sessions,
		json_each(metadata, '$.source_tool_ids') WHERE session_id = ?`,
		"identical-identity").Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 2 {
		t.Fatalf("recorded identities = %d, want only the two live rows", recorded)
	}
}

const ownershipCloudID = "fixture-codex-timing-owner"

func cloudCodexExport(agentTime string) string {
	created := ""
	if agentTime != "" {
		created = `"create_time":` + agentTime + `,`
	}
	return `[{"id":"` + ownershipCloudID + `","title":"Synthetic","turns":[
	  {"id":"u","role":"user","input_items":[{"type":"message","role":"user","content":[{"content_type":"text","text":"Open the imaginary hatch."}]}]},
	  {"id":"a","role":"assistant","previous_turn_id":"u",` + created + `"output_items":[
	    {"type":"message","role":"assistant","content":[{"content_type":"text","text":"The hatch is open."}]}]}
	]}]`
}

const ownershipRollout = `{"type":"session_meta","payload":{"id":"` + ownershipCloudID + `"}}
{"type":"event_msg","timestamp":"2026-08-01T12:00:01Z","payload":{"type":"user_message","message":"Open the imaginary hatch."}}
{"type":"event_msg","timestamp":"2026-08-01T12:00:02Z","payload":{"type":"task_complete","last_agent_message":"The hatch is open."}}
`

func writeCloudThenLocal(t *testing.T, cloud string, local string) (*sql.DB, string) {
	t.Helper()
	db := corpusDatabase(t)
	cloudRecords, err := parsers.ParseChatGPTCodex(strings.NewReader(cloud),
		parsers.FileMeta{SourceAgent: "codex-cloud"})
	if err != nil {
		t.Fatal(err)
	}
	writeHarvestRecords(t, db, cloudRecords)
	if local != "" {
		localRecords, err := parsers.Parse(parsers.KindCodexSession, []byte(local),
			parsers.FileMeta{SessionID: ownershipCloudID})
		if err != nil {
			t.Fatal(err)
		}
		writeHarvestRecords(t, db, localRecords)
	}
	var agentTS string
	if err := db.SQL().QueryRow(`SELECT COALESCE(agent_timestamp, '') FROM exchanges
		WHERE session_id = ?`, ownershipCloudID).Scan(&agentTS); err != nil {
		t.Fatal(err)
	}
	return db.SQL(), agentTS
}

func timestampOwner(t *testing.T, db *sql.DB, field string) string {
	t.Helper()
	var source, status string
	if err := db.QueryRow(`SELECT
		COALESCE(json_extract(metadata, '$.timestamp_owners."1".`+field+`.source'), ''),
		COALESCE(json_extract(metadata, '$.timestamp_owners."1".`+field+`.status'), '')
		FROM sessions WHERE session_id = ?`, ownershipCloudID).Scan(&source, &status); err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(status+":"+source, ":")
}

func TestCloudAndLocalCodexTimestampsRecordTheirOwners(t *testing.T) {
	t.Run("absent cloud timing stays unknown", func(t *testing.T) {
		db, agentTS := writeCloudThenLocal(t, cloudCodexExport(""), "")
		if agentTS != "" {
			t.Fatalf("agent timestamp = %q, want unknown", agentTS)
		}
		for _, field := range []string{"human_timestamp", "agent_timestamp"} {
			if got := timestampOwner(t, db, field); got != "unknown:" {
				t.Fatalf("%s owner = %q, want unknown", field, got)
			}
		}
	})
	t.Run("local fills what the cloud left unknown", func(t *testing.T) {
		db, agentTS := writeCloudThenLocal(t, cloudCodexExport(""), ownershipRollout)
		if agentTS != "2026-08-01T12:00:02Z" {
			t.Fatalf("agent timestamp = %q", agentTS)
		}
		for _, field := range []string{"human_timestamp", "agent_timestamp"} {
			if got := timestampOwner(t, db, field); got != "observed:codex" {
				t.Fatalf("%s owner = %q, want observed:codex", field, got)
			}
		}
	})
	t.Run("conflicting local timing keeps the cloud owner", func(t *testing.T) {
		// 1785585603 is 2026-08-01T12:00:03Z, a second after the rollout's answer.
		db, agentTS := writeCloudThenLocal(t, cloudCodexExport("1785585603"), ownershipRollout)
		if agentTS != "2026-08-01T12:00:03Z" {
			t.Fatalf("agent timestamp = %q, want the cloud's stated instant", agentTS)
		}
		if got := timestampOwner(t, db, "agent_timestamp"); got != "observed:codex-cloud" {
			t.Fatalf("agent owner = %q, want observed:codex-cloud", got)
		}
		if got := timestampOwner(t, db, "human_timestamp"); got != "observed:codex" {
			t.Fatalf("human owner = %q, want observed:codex", got)
		}
	})
}
