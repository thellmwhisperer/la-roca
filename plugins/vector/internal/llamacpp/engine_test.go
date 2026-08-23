//go:build cgo && !windows

package llamacpp

import "testing"

func TestSelectedBackendReflectsActualAcceleration(t *testing.T) {
	tests := []struct {
		layers            int
		accelerated       bool
		backend, fallback string
	}{
		{99, true, BackendMetal, ""},
		{99, false, BackendCPU, "accelerator unavailable"},
		{0, false, BackendCPU, ""},
	}
	for _, test := range tests {
		backend, fallback := selectedBackend(test.layers, test.accelerated)
		if backend != test.backend || fallback != test.fallback {
			t.Fatalf("selected backend = %q/%q, want %q/%q", backend, fallback, test.backend, test.fallback)
		}
	}
}
