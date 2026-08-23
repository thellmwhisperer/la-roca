package vector

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestLabGenerationBeforeAfter(t *testing.T) {
	if os.Getenv("ROCA_VECTOR_LAB") != "1" {
		t.Skip("set ROCA_VECTOR_LAB=1 to measure a local lab copy")
	}
	dbPath := os.Getenv("ROCA_VECTOR_LAB_DB")
	if dbPath == "" {
		t.Fatal("ROCA_VECTOR_LAB_DB is required")
	}
	targetIDs := strings.Split(os.Getenv("ROCA_VECTOR_LAB_IDS"), ",")
	if len(targetIDs) == 0 || targetIDs[0] == "" {
		t.Fatal("ROCA_VECTOR_LAB_IDS is required")
	}
	wanted := map[string]bool{}
	for _, id := range targetIDs {
		wanted[strings.TrimSpace(id)] = true
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var title, started string
	_ = db.QueryRow(`SELECT COALESCE(title,''), COALESCE(started_at,'') FROM sessions LIMIT 1`).Scan(&title, &started)
	rows, err := db.Query(`SELECT CAST(id AS TEXT), COALESCE(human_text,''), COALESCE(agent_text,'') FROM exchanges`)
	if err != nil {
		t.Fatal(err)
	}
	type exchange struct{ id, human, agent string }
	var all []exchange
	for rows.Next() {
		var item exchange
		if err := rows.Scan(&item.id, &item.human, &item.agent); err != nil {
			t.Fatal(err)
		}
		all = append(all, item)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	query := strings.TrimSpace(os.Getenv("ROCA_VECTOR_LAB_QUERY"))
	if query == "" {
		shortest := 1 << 30
		for _, item := range all {
			text := strings.TrimSpace(item.human)
			if !wanted[item.id] || text == "" {
				continue
			}
			if len(text) < shortest {
				shortest = len(text)
				query = text
			}
		}
	}
	if query == "" {
		t.Fatal("no lab query")
	}

	oldSources := make([]sourceRow, 0, len(all))
	newSources := make([]sourceRow, 0, len(all)*2)
	for _, item := range all {
		concat := strings.TrimSpace(item.human)
		if strings.TrimSpace(item.agent) != "" {
			if concat != "" {
				concat += "\n\n"
			}
			concat += item.agent
		}
		oldSources = append(oldSources, sourceRow{kind: "exchanges", sourceID: item.id, text: concat,
			chunkSize: 4000, overlap: 400})
		if strings.TrimSpace(item.human) != "" {
			newSources = append(newSources, sourceRow{kind: "exchanges", sourceID: item.id, column: "human_text",
				text: item.human, rowText: concat, title: title, occurredAt: started})
		}
		if strings.TrimSpace(item.agent) != "" {
			newSources = append(newSources, sourceRow{kind: "exchanges", sourceID: item.id, column: "agent_text",
				text: item.agent, rowText: concat, title: title, occurredAt: started})
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	embedder := Ollama{BaseURL: os.Getenv("OLLAMA_HOST")}
	oldScores, oldChunks, oldRate := labRun(t, ctx, embedder, oldSources, query, wanted)
	newScores, newChunks, newRate := labRun(t, ctx, embedder, newSources, query, wanted)
	t.Logf("chunks old=%d new=%d (%.2fx) rate old=%.1f/s new=%.1f/s",
		oldChunks, newChunks, float64(newChunks)/max(1, float64(oldChunks)), oldRate, newRate)
	var oldMean, newMean float64
	improved := 0
	for _, id := range targetIDs {
		id = strings.TrimSpace(id)
		t.Logf("target cosine old=%.3f new=%.3f", oldScores[id], newScores[id])
		oldMean += oldScores[id]
		newMean += newScores[id]
		if newScores[id] > oldScores[id] {
			improved++
		}
	}
	n := float64(len(targetIDs))
	t.Logf("mean cosine old=%.3f new=%.3f improved=%d/%d", oldMean/n, newMean/n, improved, len(targetIDs))
	if newMean <= oldMean || improved == 0 {
		t.Fatalf("lab retrieval did not improve: mean old=%.3f new=%.3f improved=%d", oldMean/n, newMean/n, improved)
	}
}

func labRun(t *testing.T, ctx context.Context, embedder Embedder, sources []sourceRow, query string, wanted map[string]bool) (map[string]float64, int, float64) {
	t.Helper()
	index := Index{Corpus: &memoryCorpus{sources: sources}, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: embedder, BatchSize: 8}
	started := time.Now()
	delta, err := index.Ingest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started).Seconds()
	rate := 0.0
	if elapsed > 0 {
		rate = float64(delta.Chunks) / elapsed
	}
	results, err := index.Query(ctx, query, 100)
	if err != nil {
		t.Fatal(err)
	}
	scores := map[string]float64{}
	for _, result := range results {
		id := result.ID
		if id == "" {
			id = result.SourceID
		}
		if i := strings.LastIndex(id, "/"); i >= 0 {
			id = id[i+1:]
		}
		if wanted[id] && result.Score > scores[id] {
			scores[id] = result.Score
		}
	}
	return scores, delta.Chunks, rate
}
