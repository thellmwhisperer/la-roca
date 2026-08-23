//go:build !windows

package vector

import "testing"

func TestMemoryHighWaterIncludesTheProcess(t *testing.T) {
	if got := memoryHighWater(); got <= 0 {
		t.Fatalf("process memory high-water = %d", got)
	}
}
