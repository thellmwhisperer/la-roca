//go:build cgo && !windows

package vector

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/llamacpp"
	"github.com/thellmwhisperer/la-roca-vector/internal/model"
)

// TestDeltaIngestTerminatesWhileResidentHoldsAccelerator is the user-path
// hang: `ingest --delta` used to open the same accelerator as `_resident`
// and then sit in a Metal wait. Indexing must finish even when a reader
// already has the engine.
func TestDeltaIngestTerminatesWhileResidentHoldsAccelerator(t *testing.T) {
	dataDir := os.Getenv("ROCA_VECTOR_LAB_DATA_DIR")
	if dataDir == "" {
		t.Skip("set ROCA_VECTOR_LAB_DATA_DIR to prove ingest against a held accelerator")
	}
	path, err := model.Existing(dataDir, model.DefaultManifest())
	if err != nil {
		t.Skip(err.Error())
	}
	resident, err := llamacpp.OpenPreferred(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resident.Close)

	federation, _, _, _ := federationFixture(t)
	writer := &Native{DataDir: dataDir, StateDir: t.TempDir(), ReadOnly: false}
	t.Cleanup(writer.Close)
	federation.Embedder = writer
	federation.Model = DefaultModel

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	delta, err := federation.Ingest(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if delta.Added == 0 {
		t.Fatalf("delta ingest wrote no embeddings: %+v", delta)
	}
	if llamacpp.GPULayers(false) != 0 {
		t.Fatal("writer still requests the accelerator")
	}
}
