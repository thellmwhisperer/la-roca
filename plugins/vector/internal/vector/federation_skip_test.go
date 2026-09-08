package vector

import "testing"

func TestSkipInteractiveSidecar(t *testing.T) {
	if !skipInteractiveSidecar("", interactiveSidecarBytes) {
		t.Fatal("unscoped oversized sidecar should skip")
	}
	if skipInteractiveSidecar("", interactiveSidecarBytes-1) {
		t.Fatal("unscoped small sidecar should search")
	}
	if skipInteractiveSidecar("corpus", interactiveSidecarBytes+1) {
		t.Fatal("explicit --databases should search even a huge sidecar")
	}
}
