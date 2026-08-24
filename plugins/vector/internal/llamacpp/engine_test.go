//go:build cgo && !windows

package llamacpp

import (
	"os"
	"testing"

	"github.com/thellmwhisperer/la-roca-vector/internal/model"
)

func TestOpenWithZeroGPULayersReportsCPU(t *testing.T) {
	dataDir := os.Getenv("ROCA_VECTOR_LAB_DATA_DIR")
	if dataDir == "" {
		t.Skip("set ROCA_VECTOR_LAB_DATA_DIR to run the native backend regression")
	}
	modelPath, err := model.Existing(dataDir, model.DefaultManifest())
	if err != nil {
		t.Skip(err.Error())
	}
	engine, err := Open(modelPath, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if engine.Backend != BackendCPU {
		t.Fatalf("zero-layer engine backend = %q, want %q", engine.Backend, BackendCPU)
	}
}

func TestSelectedBackendReflectsActualAcceleration(t *testing.T) {
	tests := []struct {
		layers            int
		accelerated       bool
		backend, fallback string
	}{
		{99, true, BackendMetal, ""},
		{99, false, BackendCPU, "accelerator unavailable"},
		{0, false, BackendCPU, ""},
		{0, true, BackendCPU, ""},
	}
	for _, test := range tests {
		backend, fallback := selectedBackend(test.layers, test.accelerated)
		if backend != test.backend || fallback != test.fallback {
			t.Fatalf("selected backend = %q/%q, want %q/%q", backend, fallback, test.backend, test.fallback)
		}
	}
}
