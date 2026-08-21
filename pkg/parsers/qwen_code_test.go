package parsers

import (
	"os"
	"testing"
)

func TestQwenCodePreservesRecordedModelAndUsage(t *testing.T) {
	content, err := os.ReadFile("testdata/conformance/qwen-code-session/session.data")
	if err != nil {
		t.Fatal(err)
	}
	records, err := Parse(KindQwenCode, content, FileMeta{SourceAgent: "qwen-code"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Sessions) != 1 || len(records.Sessions[0].Exchanges) != 1 {
		t.Fatalf("records = %+v", records)
	}
	exchange := records.Sessions[0].Exchanges[0]
	if exchange.Provenance.Model != "synthetic-lab/Quartz-7B" ||
		intOrZero(exchange.Provenance.TokensIn) != 11 ||
		intOrZero(exchange.Provenance.TokensOut) != 4 ||
		intOrZero(exchange.Provenance.TokensReasoning) != 2 {
		t.Errorf("provenance = %+v", exchange.Provenance)
	}
	if len(records.Discards) != 1 || !records.Discards[0].ByDesign {
		t.Errorf("system record accounting = %+v", records.Discards)
	}
}

func TestQwenCodeLeavesAnUnrecordedModelEmpty(t *testing.T) {
	content := []byte("" +
		`{"type":"user","sessionId":"model-empty","timestamp":"2026-08-01T09:00:00Z","cwd":"/synthetic/harbour","version":"9.9.9","message":{"role":"user","parts":[{"text":"Name the buoy."}]}}` + "\n" +
		`{"type":"assistant","sessionId":"model-empty","timestamp":"2026-08-01T09:00:01Z","cwd":"/synthetic/harbour","version":"9.9.9","message":{"role":"model","parts":[{"text":"Azure."}]}}` + "\n")
	records, err := Parse(KindQwenCode, content, FileMeta{SourceAgent: "qwen-code"})
	if err != nil {
		t.Fatal(err)
	}
	if got := records.Sessions[0].Exchanges[0].Provenance.Model; got != "" {
		t.Errorf("model = %q, want honestly empty", got)
	}
}

func TestQwenCodeDefersATruncatedToolLoop(t *testing.T) {
	content := []byte("" +
		`{"type":"user","sessionId":"truncated-loop","timestamp":"2026-08-01T09:00:00Z","cwd":"/synthetic/harbour","version":"9.9.9","message":{"role":"user","parts":[{"text":"Chart the invented buoy."}]}}` + "\n" +
		`{"type":"assistant","sessionId":"truncated-loop","timestamp":"2026-08-01T09:00:01Z","cwd":"/synthetic/harbour","version":"9.9.9","model":"synthetic-lab/Quartz-7B","message":{"role":"model","parts":[{"functionCall":{"id":"synthetic-call","name":"chart_buoy","args":{"sector":"blue"}}}]}}` + "\n" +
		`{"type":"tool_result","sessionId":"truncated-loop","timestamp":"2026-08-01T09:00:02Z","cwd":"/synthetic/harbour","version":"9.9.9","toolCallResult":{"status":"success"},"message":{"role":"user","parts":[{"functionResponse":{"id":"synthetic-call","name":"chart_buoy","response":{"output":"charted"}}}]}}` + "\n")
	records, err := Parse(KindQwenCode, content, FileMeta{SourceAgent: "qwen-code"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(records.Sessions[0].Exchanges); got != 0 {
		t.Errorf("exchanges = %d, want the incomplete turn omitted", got)
	}
	if records.Deferred != 1 {
		t.Errorf("deferred = %d, want 1", records.Deferred)
	}
}
