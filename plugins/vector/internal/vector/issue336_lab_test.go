package vector

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func issue336LabHome() string {
	if home := os.Getenv("ROCA_ISSUE336_LAB"); home != "" {
		return home
	}
	return "/Volumes/CrucialX9/workspace/firstmate/data/roca-bug-336-corpus-sidecar-s1/lab-home"
}

func TestIssue336LabCopyPublishedStatusFailsAndBranchReportsCounts(t *testing.T) {
	home := issue336LabHome()
	pluginRoot := filepath.Join(home, ".roca", "plugins")
	sidecar := filepath.Join(pluginRoot, "roca-corpus", "roca-corpus.vector.db")
	if _, err := os.Stat(sidecar); err != nil {
		t.Skip("lab copy of the corpus sidecar is not on this machine")
	}

	published := exec.Command("roca", "vector", "status", "--json")
	published.Env = append(os.Environ(), "HOME="+home)
	started := time.Now()
	out, err := published.CombinedOutput()
	publishedDur := time.Since(started)
	if err != nil {
		t.Fatalf("published status: %v\n%s", err, out)
	}
	var pub struct {
		Databases []DatabaseVectorization `json:"databases"`
	}
	if err := json.Unmarshal(out, &pub); err != nil {
		t.Fatalf("published status json: %v\n%s", err, out)
	}
	pubCorpus := labRow(pub.Databases, "roca-corpus")
	t.Logf("published status %s corpus embedded=%v candidates=%v state=%q lock=%q compact=%v bytes=%v",
		publishedDur, pubCorpus.EmbeddedChunks, pubCorpus.CandidateChunks, pubCorpus.State,
		pubCorpus.IndexLock, pubCorpus.CompactRecommended, pubCorpus.SidecarBytes)
	if pubCorpus.EmbeddedChunks != nil {
		t.Fatalf("published binary reported corpus embedded=%v; the defect is that this stays unknown", *pubCorpus.EmbeddedChunks)
	}
	if pubCorpus.State != StateUnknown {
		t.Fatalf("published corpus state=%q, want unknown", pubCorpus.State)
	}

	started = time.Now()
	report, err := ReportVectorization(context.Background(), StatusRequest{PluginRoot: pluginRoot})
	branchDur := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	branchCorpus := labRow(report.Databases, "roca-corpus")
	t.Logf("branch status %s corpus embedded=%d candidates=%v state=%q lock=%q compact=%v bytes=%d",
		branchDur, valueOrZero(branchCorpus.EmbeddedChunks), branchCorpus.CandidateChunks, branchCorpus.State,
		branchCorpus.IndexLock, branchCorpus.CompactRecommended, valueOrZero(branchCorpus.SidecarBytes))
	if branchCorpus.EmbeddedChunks == nil || *branchCorpus.EmbeddedChunks < 1_000_000 {
		t.Fatalf("branch corpus embedded=%v, want the live 3.89M-scale count", branchCorpus.EmbeddedChunks)
	}
	if branchCorpus.State != StateComplete {
		t.Fatalf("branch corpus state=%q, want complete", branchCorpus.State)
	}
	if branchCorpus.IndexLock != IndexLockStale {
		t.Fatalf("branch corpus index.lock=%q, want stale", branchCorpus.IndexLock)
	}
	if branchCorpus.CompactRecommended {
		t.Fatal("branch recommended compact for a healthy ~4KB/chunk sidecar")
	}
	if publishedDur > 10*time.Second {
		t.Fatalf("published status took %s", publishedDur)
	}

	opsSidecar := filepath.Join(pluginRoot, "roca-ops", "roca-ops.vector.db")
	opsQuery := labQueryDuration(t, opsSidecar)
	corpusCold := labQueryDuration(t, sidecar)
	corpusWarm := labQueryDuration(t, sidecar)
	t.Logf("branch isolated query (stub embedder, no #335 model load) ops=%s corpus_cold=%s corpus_warm=%s",
		opsQuery, corpusCold, corpusWarm)
	// The remaining corpus gap is sqlite-vec kNN over ~3.9M chunks, not the
	// published full-table chunk scan. Warm ANN should stay well under that scan.
	if corpusWarm > 20*time.Second {
		t.Fatalf("branch corpus warm query %s still looks like a full chunk-table scan", corpusWarm)
	}
}

func labRow(rows []DatabaseVectorization, plugin string) DatabaseVectorization {
	for _, row := range rows {
		if row.Plugin == plugin {
			return row
		}
	}
	return DatabaseVectorization{}
}

type fixedDimEmbedder struct{ dim int }

func (fixedDimEmbedder) Pull(context.Context, string) error { return nil }

func (e fixedDimEmbedder) Embed(_ context.Context, _ string, input []string) ([][]float32, error) {
	out := make([][]float32, len(input))
	for i := range input {
		vec := make([]float32, e.dim)
		if e.dim > 0 {
			vec[0] = 1
		}
		out[i] = vec
	}
	return out, nil
}

type staticCorpus struct{}

func (staticCorpus) WalkSources(context.Context, string, func(sourceRow) error) error { return nil }

func (staticCorpus) ResolveSource(context.Context, string, locator) (string, error) {
	return "lab snippet", nil
}

func labQueryDuration(t *testing.T, sidecar string) time.Duration {
	t.Helper()
	store, err := openSQLite(sidecar, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, dim, err := readIndexMeta(store)
	if err != nil || dim == 0 {
		t.Fatalf("lab sidecar meta dim=%d err=%v", dim, err)
	}
	index := Index{Corpus: staticCorpus{}, VectorPath: sidecar, Model: DefaultModel,
		Embedder: fixedDimEmbedder{dim: dim}, Database: "lab"}
	started := time.Now()
	results, err := index.Query(context.Background(), "member of technical staff", 5)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatalf("lab query on %s returned no hits", sidecar)
	}
	return elapsed
}
