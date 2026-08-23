package telemetry

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestStoreRecordsOperationalEventsWithoutContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records := []Record{
		{Kind: KindLoad, Backend: "metal", DurationMS: 299, MemoryHWM: 700 << 20},
		{Kind: KindPrewarm, Backend: "metal", DurationMS: 301},
		{Kind: KindEmbed, Backend: "metal", DurationMS: 18, BatchSize: 1},
		{Kind: KindBatch, Backend: "cpu", DurationMS: 1200, BatchSize: 64, Throughput: 53.3, Fallback: "metal init failed"},
		{Kind: KindError, Backend: "cpu", Err: "the embedding model is not downloaded"},
	}
	for _, record := range records {
		if err := store.Record(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT kind, backend, duration_ms, batch_size, throughput, memory_hwm_bytes, fallback_reason, error FROM engine_telemetry ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind, backend, fallback, message string
		var duration, batch, memory sql.NullInt64
		var throughput sql.NullFloat64
		if err := rows.Scan(&kind, &backend, &duration, &batch, &throughput, &memory, &fallback, &message); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
		blob := strings.ToLower(kind + backend + fallback + message)
		if strings.Contains(blob, "search_document") || strings.Contains(blob, "search_query") ||
			strings.Contains(blob, "why should i") {
			t.Fatalf("telemetry stored content: %q", blob)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(kinds, ",") != "load,prewarm,embed,batch,error" {
		t.Fatalf("kinds = %q", kinds)
	}
}

func TestStoreIsQueryableByKindAndTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Record(context.Background(), Record{Kind: KindLoad, Backend: "cpu", DurationMS: 10}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := store.Record(context.Background(), Record{Kind: KindEmbed, Backend: "cpu", DurationMS: 5, BatchSize: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Query(context.Background(), `SELECT kind FROM engine_telemetry WHERE kind = 'embed'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["kind"] != "embed" {
		t.Fatalf("query = %+v", got)
	}
}
