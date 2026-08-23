package engine

import (
	"strings"
	"testing"
	"time"
)

func TestEventEnvelopeCarriesEveryKind(t *testing.T) {
	seen := map[Kind]bool{}
	for _, event := range []Event{
		Progress("download", "downloading the embedding model", 10, 100, 2*time.Second),
		Partial("ingest", "indexing recent material", "2026-08", 4, 20, time.Minute),
		Result("prewarm", "semantic search: ready"),
		Error("embed", "the embedding model is not downloaded"),
		Cancel("ingest", "semantic search cancelled"),
	} {
		if event.Kind == "" || event.Stage == "" {
			t.Fatalf("event missing kind or stage: %+v", event)
		}
		if event.Line() == "" {
			t.Fatalf("event has no product line: %+v", event)
		}
		seen[event.Kind] = true
	}
	for _, kind := range []Kind{KindProgress, KindPartial, KindResult, KindError, KindCancel} {
		if !seen[kind] {
			t.Fatalf("missing kind %s", kind)
		}
	}
}

func TestProductLinesOmitEngineInternals(t *testing.T) {
	lines := []string{
		Progress("download", "downloading the embedding model · 34% · 325 MB of 913 MB", 325, 913, 0).Line(),
		Partial("ingest", "semantic index: 64 added · 2026-08", "2026-08", 64, 1000, 0).Line(),
		Result("prewarm", "semantic search: ready").Line(),
		Error("embed", "the embedding model is not downloaded").Line(),
	}
	forbidden := []string{"ollama", "gguf", "llama.cpp", "metal", "cgo", "/Users/", "/Volumes/"}
	for _, line := range lines {
		lower := strings.ToLower(line)
		for _, word := range forbidden {
			if strings.Contains(lower, strings.ToLower(word)) {
				t.Fatalf("product line leaked %q: %q", word, line)
			}
		}
	}
}

func TestPercentAndByteFormatting(t *testing.T) {
	if Percent(0, 100) != 0 || Percent(50, 100) != 50 || Percent(100, 100) != 100 {
		t.Fatalf("percent = %d %d %d", Percent(0, 100), Percent(50, 100), Percent(100, 100))
	}
	if Percent(10, 0) != 0 {
		t.Fatal("zero total must not divide")
	}
	if got := FormatBytes(957680480); !strings.Contains(got, "MB") && !strings.Contains(got, "GB") {
		t.Fatalf("bytes = %q", got)
	}
}
