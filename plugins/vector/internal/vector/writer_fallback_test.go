//go:build !windows

package vector

import "testing"

func TestWriterFallbackReasonIsDarwinSpecific(t *testing.T) {
	tests := []struct {
		readers bool
		backend, existing, want string
	}{
		{true, "cpu", "", "indexing leaves the accelerator for live search"},
		{false, "cpu", "", ""},
		{true, "cpu", "accelerator init failed", "accelerator init failed"},
		{true, "metal", "", ""},
	}
	for _, test := range tests {
		got := writerFallbackReason(test.readers, test.backend, test.existing)
		if got != test.want {
			t.Fatalf("writerFallbackReason(%v, %q, %q) = %q, want %q",
				test.readers, test.backend, test.existing, got, test.want)
		}
	}
}
