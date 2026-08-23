package telemetry

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreRecordsJSONLWithoutContentOrDatabase(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records := []Record{
		{Kind: KindLoad, Backend: "metal", DurationMS: 299, MemoryHWM: 700 << 20},
		{Kind: KindPrewarm, Backend: "metal", DurationMS: 301},
		{Kind: KindEmbed, Operation: OperationQuery, Backend: "metal", DurationMS: 18, BatchSize: 1},
		{Kind: KindBatch, Backend: "cpu", DurationMS: 1200, BatchSize: 64, Throughput: 53.3, Fallback: "accelerator init failed"},
		{Kind: KindError, Backend: "cpu", Err: "the embedding model is not downloaded"},
	}
	for _, record := range records {
		if err := store.Record(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	read, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if read[2].Operation != OperationQuery {
		t.Fatalf("query operation = %q", read[2].Operation)
	}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".db") {
			t.Errorf("telemetry created a database file: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := currentLog(t, root)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var kinds []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		kinds = append(kinds, field(t, line, "kind"))
		lower := strings.ToLower(line)
		for _, leaked := range []string{"search_document", "search_query", "why should i", "create table"} {
			if strings.Contains(lower, leaked) {
				t.Fatalf("telemetry log leaked %q: %s", leaked, line)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(kinds, ",") != "load,prewarm,embed,batch,error" {
		t.Fatalf("kinds = %q", kinds)
	}
}

func TestStoreIsAnalyzableByReadingTheLogFiles(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
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
	got, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	var embeds int
	for _, record := range got {
		if record.Kind == KindEmbed {
			embeds++
			if record.DurationMS != 5 || record.BatchSize != 1 {
				t.Fatalf("embed record = %+v", record)
			}
		}
	}
	if embeds != 1 || len(got) != 2 {
		t.Fatalf("read = %+v", got)
	}
}

func TestStoreRotatesOversizedLogs(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.maxFileBytes = 200
	for i := 0; i < 20; i++ {
		if err := store.Record(context.Background(), Record{Kind: KindEmbed, Backend: "cpu", DurationMS: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	matches, err := filepath.Glob(filepath.Join(Dir(root), Stream+"-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) < 2 {
		t.Fatalf("rotation produced %d files, want at least a live file and an archive", len(matches))
	}
}

func TestIndependentStoresSerializeRotationAndAppend(t *testing.T) {
	root := t.TempDir()
	first, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range []*Store{first, second} {
		store.maxFileBytes = 180
		store.maxFiles = 200
	}
	const recordsPerStore = 40
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for storeIndex, store := range []*Store{first, second} {
		wait.Add(1)
		go func(storeIndex int, store *Store) {
			defer wait.Done()
			for index := 0; index < recordsPerStore; index++ {
				if err := store.Record(context.Background(), Record{
					Kind: KindEmbed, Backend: "cpu", DurationMS: int64(storeIndex*recordsPerStore + index),
				}); err != nil {
					errors <- err
					return
				}
			}
		}(storeIndex, store)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	records, err := first.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2*recordsPerStore {
		t.Fatalf("concurrent records = %d, want %d", len(records), 2*recordsPerStore)
	}
}

func currentLog(t *testing.T, root string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(Dir(root), Stream+"-*.jsonl"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no engine log under %s: %v %q", root, err, matches)
	}
	return matches[0]
}

func field(t *testing.T, line, key string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("jsonl %q: %v", line, err)
	}
	value, _ := payload[key].(string)
	return value
}
