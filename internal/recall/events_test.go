package recall

import (
	"strings"
	"testing"
)

func TestReadKeepsQueryAndSessionProvenance(t *testing.T) {
	events, err := Read(strings.NewReader(`{"ts":"2026-09-22T10:00:00Z","action":"agent spawn","tool":"Agent","query_sha":"abc123","query":"preserve the decision","session_id":"session-1","exchange_id":42,"hits":1,"ids":["7"],"elapsed_ms":12}` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Query != "preserve the decision" || event.SessionID != "session-1" || event.ExchangeID == nil || *event.ExchangeID != 42 {
		t.Fatalf("provenance = %+v", event)
	}
	if len(event.EventSHA) != 64 {
		t.Fatalf("event sha = %q, want sha256", event.EventSHA)
	}
}

func TestReadAcceptsLegacyRecordWithoutNewProvenance(t *testing.T) {
	events, err := Read(strings.NewReader(`{"ts":"2026-09-22T10:00:00Z","action":"","query_sha":"abc123","hits":0,"ids":[],"elapsed_ms":0}` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Query != "" || events[0].SessionID != "" {
		t.Fatalf("legacy event = %+v", events)
	}
}

func TestReadIgnoresIncompleteTrailingRecord(t *testing.T) {
	input := `{"ts":"2026-09-22T10:00:00Z","action":"agent spawn","query_sha":"abc123"}` + "\n" +
		`{"ts":"2026-09-22T10:01:00Z","action":"agent spawn","query_sha":"def456"`
	events, err := Read(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].QuerySHA != "abc123" {
		t.Fatalf("events = %+v", events)
	}
}

func TestReadRejectsMalformedCompleteRecord(t *testing.T) {
	_, err := Read(strings.NewReader(`{"ts":"2026-09-22T10:00:00Z","action":"agent spawn","query_sha":"abc123",}`))
	if err == nil {
		t.Fatal("malformed complete record unexpectedly accepted")
	}
}
