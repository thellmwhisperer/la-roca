package llamacpp

import (
	"runtime"
	"testing"
)

func TestGPULayersKeepsWritersOffTheAccelerator(t *testing.T) {
	if GPULayers(false) != 0 {
		t.Fatalf("writer gpu layers = %d, want 0", GPULayers(false))
	}
	wantReader := 0
	if runtime.GOOS == "darwin" {
		wantReader = 99
	}
	if GPULayers(true) != wantReader {
		t.Fatalf("reader gpu layers = %d, want %d", GPULayers(true), wantReader)
	}
}
