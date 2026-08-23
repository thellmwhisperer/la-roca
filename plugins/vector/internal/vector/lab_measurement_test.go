package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type labExchange struct {
	ID         string `json:"id"`
	Human      string `json:"human"`
	AgentSeed  string `json:"agent_seed"`
	AgentWords int    `json:"agent_words"`
}

type labFixture struct {
	Title     string        `json:"title"`
	Started   string        `json:"started_at"`
	Query     string        `json:"query"`
	TargetIDs []string      `json:"target_ids"`
	Exchanges []labExchange `json:"exchanges"`
}

func TestLabGenerationBeforeAfter(t *testing.T) {
	if os.Getenv("ROCA_VECTOR_LAB") != "1" {
		t.Skip("set ROCA_VECTOR_LAB=1 to measure a local lab copy")
	}
	dbPath := strings.TrimSpace(os.Getenv("ROCA_VECTOR_LAB_DB"))
	var title, started, fixtureQuery string
	var all []labExchange
	var targetIDs []string
	if dbPath == "" {
		fixture := readLabFixture(t)
		title, started, fixtureQuery = fixture.Title, fixture.Started, fixture.Query
		all, targetIDs = fixture.Exchanges, fixture.TargetIDs
	} else {
		title, started, all = readLabDatabase(t, dbPath)
		targetIDs = strings.Split(os.Getenv("ROCA_VECTOR_LAB_IDS"), ",")
		if len(targetIDs) == 0 || targetIDs[0] == "" {
			t.Fatal("ROCA_VECTOR_LAB_IDS is required with ROCA_VECTOR_LAB_DB")
		}
	}
	wanted := map[string]bool{}
	for _, id := range targetIDs {
		wanted[strings.TrimSpace(id)] = true
	}
	query := strings.TrimSpace(os.Getenv("ROCA_VECTOR_LAB_QUERY"))
	if query == "" {
		query = fixtureQuery
	}
	if query == "" {
		query = shortestTargetText(all, wanted)
	}
	if query == "" {
		t.Fatal("no lab query")
	}

	oldSources := make([]sourceRow, 0, len(all))
	newSources := make([]sourceRow, 0, len(all)*2)
	for _, item := range all {
		agent := labAgentText(item)
		concat := strings.TrimSpace(item.Human)
		if strings.TrimSpace(agent) != "" {
			if concat != "" {
				concat += "\n\n"
			}
			concat += agent
		}
		oldSources = append(oldSources, sourceRow{kind: "exchanges", sourceID: item.ID, text: concat,
			chunkSize: 4000, overlap: 400})
		if strings.TrimSpace(item.Human) != "" {
			newSources = append(newSources, sourceRow{kind: "exchanges", sourceID: item.ID, column: "human_text",
				text: item.Human, rowText: concat, title: title, occurredAt: started})
		}
		if strings.TrimSpace(agent) != "" {
			newSources = append(newSources, sourceRow{kind: "exchanges", sourceID: item.ID, column: "agent_text",
				text: agent, rowText: concat, title: title, occurredAt: started})
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

func readLabFixture(t *testing.T) labFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "lab_measurement.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture labFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Exchanges) == 0 || len(fixture.TargetIDs) == 0 {
		t.Fatal("sanitized lab fixture has no exchanges or targets")
	}
	return fixture
}

func readLabDatabase(t *testing.T, path string) (string, string, []labExchange) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
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
	defer rows.Close()
	var exchanges []labExchange
	for rows.Next() {
		var item labExchange
		var agent string
		if err := rows.Scan(&item.ID, &item.Human, &agent); err != nil {
			t.Fatal(err)
		}
		item.AgentSeed = agent
		exchanges = append(exchanges, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return title, started, exchanges
}

func shortestTargetText(exchanges []labExchange, wanted map[string]bool) string {
	shortest := 1 << 30
	var query string
	for _, item := range exchanges {
		text := strings.TrimSpace(item.Human)
		if !wanted[item.ID] || text == "" {
			continue
		}
		if len(text) < shortest {
			shortest = len(text)
			query = text
		}
	}
	return query
}

func labAgentText(item labExchange) string {
	if item.AgentWords <= 0 {
		return item.AgentSeed
	}
	words := strings.Fields(item.AgentSeed)
	if len(words) == 0 {
		return ""
	}
	out := make([]string, item.AgentWords)
	for i := range out {
		out[i] = words[i%len(words)]
	}
	return strings.Join(out, " ")
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
