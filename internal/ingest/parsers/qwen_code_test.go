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
