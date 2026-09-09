package vector

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestFederationDefaultSearchIncludesLargeSidecars(t *testing.T) {
	federation, corpusPath, _, _ := federationFixture(t)
	if _, err := federation.Ingest(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	// Extend only the lab sidecar with sparse trailing bytes. SQLite still reads
	// the same prepared index; the file crosses the former exclusion boundary.
	for _, size := range []int64{1<<30 - 1, 1 << 30, 1<<30 + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			if err := os.Truncate(SidecarPath(corpusPath), size); err != nil {
				t.Fatal(err)
			}
			for _, expanded := range []bool{false, true} {
				query := func(databases string) (FederatedQuery, error) {
					if expanded {
						return federation.QueryExpanded(t.Context(), "remembered", 100, databases, 0)
					}
					return federation.Query(t.Context(), "remembered", 100, databases)
				}
				explicit, err := query("corpus,ops")
				if err != nil {
					t.Fatal(err)
				}
				defaultQuery, err := query("")
				if err != nil {
					t.Fatal(err)
				}
				seen := map[string]bool{}
				for _, hit := range defaultQuery.Results {
					seen[hit.Database] = true
				}
				if !defaultQuery.VectorExecuted || !seen["corpus"] || !seen["ops"] ||
					!reflect.DeepEqual(defaultQuery.Results, explicit.Results) {
					t.Fatalf("expanded=%t default results differ from explicit prepared databases: default=%+v explicit=%+v",
						expanded, defaultQuery.Results, explicit.Results)
				}
			}
		})
	}
}

func TestFederationReportsSidecarOpenFailures(t *testing.T) {
	for _, stage := range []string{"metadata", "search"} {
		t.Run(stage, func(t *testing.T) {
			federation, corpusPath, _, embedder := federationFixture(t)
			if _, err := federation.Ingest(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			breakSidecar := func() {
				path := SidecarPath(corpusPath)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "metadata" {
				breakSidecar()
			} else {
				// The metadata probe succeeds, then the query-time open fails.
				federation.Embedder = &beforeQueryEmbedder{Embedder: embedder, before: breakSidecar}
			}
			_, err := federation.Query(t.Context(), "remembered", 100, "")
			if err == nil || !strings.Contains(err.Error(), "open vector sidecar roca-corpus/corpus") {
				t.Fatalf("%s open returned partial success instead of its error: %v", stage, err)
			}
			_, explicitErr := federation.Query(t.Context(), "remembered", 100, "corpus,ops")
			if explicitErr == nil || explicitErr.Error() != err.Error() {
				t.Fatalf("%s open errors differ by scope: default=%v explicit=%v", stage, err, explicitErr)
			}
		})
	}
}

type beforeQueryEmbedder struct {
	Embedder
	before func()
}

func (e *beforeQueryEmbedder) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	if e.before != nil {
		e.before()
		e.before = nil
	}
	return e.Embedder.Embed(ctx, model, texts)
}
