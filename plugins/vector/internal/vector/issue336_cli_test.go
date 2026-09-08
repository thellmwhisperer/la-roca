package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the shipped status/compact commands against a real sparse sidecar.
// Only embeddings are synthetic; SQLite storage, locks and retrieval are real.
func TestIssue336CLICompactsStaleSidecarWithoutLosingResults(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	binary := filepath.Join(root, "roca-vector")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/roca-vector")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build companion: %v\n%s", err, output)
	}
	plugins := filepath.Join(root, "plugins")
	if err := os.MkdirAll(plugins, 0o700); err != nil {
		t.Fatal(err)
	}
	database := vectorDatabase{Plugin: "roca-corpus", Database: "corpus",
		Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{{Name: "memories", IDColumn: "id", TextColumns: []string{"content"}}}}
	writeRegistry(t, plugins, vectorRegistry{Schema: 2, Databases: []vectorDatabase{database}})
	source := filepath.Join(plugins, database.Plugin, database.Path)
	writeSourceRows(t, source, `CREATE TABLE memories(id TEXT PRIMARY KEY, content TEXT);
		INSERT INTO memories VALUES ('0','alpha memory');`)
	rows := make([]sourceRow, 4097)
	for i := range rows {
		rows[i] = sourceRow{kind: "memories", sourceID: fmt.Sprint(i), text: "alpha memory"}
	}
	corpus := &memoryCorpus{sources: rows}
	index := Index{Corpus: corpus, VectorPath: SidecarPath(source), Model: DefaultModel,
		Embedder: &recordingEmbedder{}, Database: "corpus"}
	if _, err := index.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	corpus.sources = rows[:1]
	if _, err := index.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index.VectorPath+".index.lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var transcript strings.Builder
	transcript.WriteString("Synthetic corpus: 4097 chunks ingested, then 4096 removed. Real SQLite sidecar; deterministic embeddings.\n")
	run := func(args ...string) []byte {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Env = append(os.Environ(), "HOME="+root,
			"ROCA_VECTOR_PLUGIN_ROOT="+plugins,
			"ROCA_VECTOR_STATE_DIR="+filepath.Join(root, "state"),
			"ROCA_VECTOR_ROCA_BINARY="+filepath.Join(root, "unused-core"))
		started := time.Now()
		output, err := command.CombinedOutput()
		fmt.Fprintf(&transcript, "\n$ roca-vector %s\n%sElapsed: %s\n", strings.Join(args, " "), output, time.Since(started))
		if err != nil {
			t.Fatalf("CLI: %v\n%s", err, transcript.String())
		}
		return output
	}
	status := func() DatabaseVectorization {
		t.Helper()
		var report Vectorization
		if err := json.Unmarshal(run("status", "--json"), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Databases) != 1 {
			t.Fatalf("status databases: %+v", report.Databases)
		}
		return report.Databases[0]
	}
	before := status()
	if valueOrZero(before.EmbeddedChunks) != 1 || valueOrZero(before.CandidateChunks) != 1 ||
		before.IndexLock != IndexLockStale || !before.CompactRecommended {
		t.Fatalf("sparse sidecar status: %+v", before)
	}
	text := string(run("status"))
	if !strings.Contains(text, "roca vector compact") || !strings.Contains(text, "index.lock is stale") {
		t.Fatalf("missing user remedies: %s", text)
	}
	query := func(stage string) []Result {
		t.Helper()
		started := time.Now()
		hits, err := index.Query(ctx, "alpha", 5)
		if err != nil || len(hits) != 1 {
			t.Fatalf("%s query: hits=%+v err=%v", stage, hits, err)
		}
		encoded, _ := json.Marshal(hits)
		fmt.Fprintf(&transcript, "\nQuery API (%s, fixed embedder): %s\nElapsed: %s\n", stage, encoded, time.Since(started))
		return hits
	}
	first := query("before compact")
	var compact CompactReport
	if err := json.Unmarshal(run("compact", "--json"), &compact); err != nil {
		t.Fatal(err)
	}
	if compact.BytesReclaimed <= 0 || compact.PagesAfter >= compact.PagesBefore {
		t.Fatalf("compact reclaimed no pages: %+v", compact)
	}
	after := status()
	if valueOrZero(after.EmbeddedChunks) != 1 || after.CompactRecommended ||
		valueOrZero(after.SidecarBytes) >= valueOrZero(before.SidecarBytes) {
		t.Fatalf("compacted sidecar status: %+v", after)
	}
	last := query("after compact")
	firstJSON, _ := json.Marshal(first)
	lastJSON, _ := json.Marshal(last)
	if string(firstJSON) != string(lastJSON) {
		t.Fatalf("compaction changed results: %s -> %s", firstJSON, lastJSON)
	}
	t.Log(transcript.String())
	if path := os.Getenv("ROCA_ISSUE336_EVIDENCE"); path != "" {
		if err := os.WriteFile(path, []byte(transcript.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
