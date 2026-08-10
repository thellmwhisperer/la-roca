package parsers

import "strings"
import "testing"

// A Pi file is a tree: the header, then entries linked by parentId. Only the
// branch that ends at the last entry is the conversation that happened.
const piSession = `{"type":"session","version":3,"id":"pi-77","cwd":"/w/demo","timestamp":"2026-08-01T13:00:00Z"}
{"id":"e1","parentId":null,"type":"message","timestamp":"2026-08-01T13:00:01Z","message":{"role":"user","content":"count the adapters","timestamp":"2026-08-01T13:00:01Z"}}
{"id":"e2","parentId":"e1","type":"message","timestamp":"2026-08-01T13:00:02Z","message":{"role":"assistant","stopReason":"tool","content":[{"type":"thinking","thinking":"we have to count"},{"type":"toolCall","id":"tc1","name":"grep"}]}}
{"id":"e3","parentId":"e2","type":"message","timestamp":"2026-08-01T13:00:03Z","message":{"role":"toolResult","toolCallId":"tc1","isError":false}}
{"id":"e4","parentId":"e3","type":"message","timestamp":"2026-08-01T13:00:04Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"nine"}],"timestamp":"2026-08-01T13:00:04Z"}}
`

func TestPiSessionProjectsTheActiveBranch(t *testing.T) {
	records, err := Parse(KindPiSession, []byte(piSession), FileMeta{Path: "/w/.pi/agent/sessions/-w-demo/a.jsonl"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	session := records.Sessions[0]
	// The id is namespaced: a Pi session and a Claude one can share a native id
	// and they are not the same conversation.
	if session.ID != "pi:pi-77" {
		t.Errorf("session id = %q, want pi:pi-77", session.ID)
	}
	if session.Project != "demo" {
		t.Errorf("project = %q, want demo", session.Project)
	}
	if len(session.Exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(session.Exchanges))
	}
	exchange := session.Exchanges[0]
	if exchange.HumanText != "count the adapters" || exchange.AgentText != "nine" {
		t.Errorf("exchange = %+v", exchange)
	}
	// Pi keys its exchanges on the source's own id, so re-reading a grown file
	// recognizes what already landed instead of renumbering it.
	if exchange.SourceID != "e1" || exchange.Fingerprint == "" {
		t.Errorf("identity = %q / %q", exchange.SourceID, exchange.Fingerprint)
	}
	if len(exchange.Thinking) != 1 || len(exchange.Tools) != 1 {
		t.Fatalf("children = %+v / %+v", exchange.Thinking, exchange.Tools)
	}
	if exchange.Tools[0].Name != "grep" || exchange.Tools[0].HadError {
		t.Errorf("tool = %+v", exchange.Tools[0])
	}
	if exchange.LatencyMS == nil || *exchange.LatencyMS != 3000 {
		t.Errorf("latency = %v, want 3000", exchange.LatencyMS)
	}
}

func TestPiFingerprintIsStableAndChangesWithTheTurn(t *testing.T) {
	first, err := Parse(KindPiSession, []byte(piSession), FileMeta{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	again, err := Parse(KindPiSession, []byte(piSession), FileMeta{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if first.Sessions[0].Exchanges[0].Fingerprint != again.Sessions[0].Exchanges[0].Fingerprint {
		t.Error("the same turn hashed differently twice")
	}
	changed, err := Parse(KindPiSession, []byte(strings.Replace(piSession, "nine", "diez", 1)), FileMeta{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if first.Sessions[0].Exchanges[0].Fingerprint == changed.Sessions[0].Exchanges[0].Fingerprint {
		t.Error("a turn whose answer changed hashed the same")
	}
}

func TestPiTurnWithAnUnansweredToolIsDeferredAndNotIngested(t *testing.T) {
	content := `{"type":"session","version":3,"id":"pi-78","cwd":"/w/demo","timestamp":"2026-08-01T13:00:00Z"}
{"id":"e1","parentId":null,"type":"message","message":{"role":"user","content":"arranca"}}
{"id":"e2","parentId":"e1","type":"message","message":{"role":"assistant","stopReason":"stop","content":[{"type":"toolCall","id":"tc1","name":"grep"}]}}
`
	records, err := Parse(KindPiSession, []byte(content), FileMeta{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records.Sessions[0].Exchanges) != 0 {
		t.Errorf("a turn whose call has no result was ingested: %+v", records.Sessions[0].Exchanges)
	}
	if records.Deferred != 1 {
		t.Errorf("deferred = %d, want 1", records.Deferred)
	}
}

func TestPiHoldsBackTheLineItIsStillWriting(t *testing.T) {
	// No newline behind the last line: Pi is writing it right now.
	content := piSession + `{"id":"e5","parentId":"e4","type":"message","message":{"role":"user","cont`
	records, err := Parse(KindPiSession, []byte(content), FileMeta{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if records.Deferred != 1 {
		t.Errorf("deferred = %d, want 1", records.Deferred)
	}
	if len(records.Sessions[0].Exchanges) != 1 {
		t.Errorf("exchanges = %d, want 1", len(records.Sessions[0].Exchanges))
	}
}

func TestPiRefusesWhatItCannotReadWithoutGuessing(t *testing.T) {
	cases := map[string]string{
		"another version": `{"type":"session","version":2,"id":"p","cwd":"/w"}` + "\n",
		"no header":       `{"id":"e1","parentId":null,"type":"message"}` + "\n",
		"relative cwd":    `{"type":"session","version":3,"id":"p","cwd":"demo"}` + "\n",
		"two roots": `{"type":"session","version":3,"id":"p","cwd":"/w"}` + "\n" +
			`{"id":"a","parentId":null,"type":"message"}` + "\n" +
			`{"id":"b","parentId":null,"type":"message"}` + "\n",
		"missing parent": `{"type":"session","version":3,"id":"p","cwd":"/w"}` + "\n" +
			`{"id":"a","parentId":null,"type":"message"}` + "\n" +
			`{"id":"b","parentId":"ghost","type":"message"}` + "\n",
		"the last entry is not a leaf": `{"type":"session","version":3,"id":"p","cwd":"/w"}` + "\n" +
			`{"id":"a","parentId":null,"type":"message"}` + "\n" +
			`{"id":"b","parentId":"a","type":"message"}` + "\n" +
			`{"id":"a2","parentId":"a","type":"message"}` + "\n" +
			`{"id":"a","parentId":null,"type":"message"}` + "\n",
	}
	for name, content := range cases {
		if _, err := Parse(KindPiSession, []byte(content), FileMeta{}); err == nil {
			t.Errorf("%s: accepted without complaining", name)
		}
	}
}

func TestPiHeaderAloneIsASessionWithNothingInItYet(t *testing.T) {
	records, err := Parse(KindPiSession,
		[]byte(`{"type":"session","version":3,"id":"pi-79","cwd":"/w/demo","timestamp":"2026-08-01T13:00:00Z"}`+"\n"),
		FileMeta{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records.Sessions) != 1 || len(records.Sessions[0].Exchanges) != 0 {
		t.Fatalf("records = %+v", records)
	}
}
