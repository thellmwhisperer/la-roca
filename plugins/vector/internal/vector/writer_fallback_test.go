//go:build !windows

package vector

import (
	"testing"

	"github.com/thellmwhisperer/la-roca-vector/internal/llamacpp"
)

// The writer's fallback_reason says which backend ran and why, so a slow run is
// diagnosable from the log. What the engine already knows wins: an accelerator
// that refused to start is a better answer than the policy that asked for it.
func TestWriterFallbackReasonReportsThePolicyUnlessTheEngineKnowsBetter(t *testing.T) {
	tests := []struct {
		name                    string
		policy                  string
		existing, backend, want string
	}{
		{
			name:    "conservative delta explains the quiet cpu run",
			policy:  "indexing leaves the accelerator for live search",
			backend: "cpu",
			want:    "indexing leaves the accelerator for live search",
		},
		{
			name:    "a bulk build says it accelerated on purpose",
			policy:  "bulk build default",
			backend: "metal",
			want:    "bulk build default",
		},
		{
			name:    "the lever is named when the operator pulled it",
			policy:  "operator requested accelerator",
			backend: "metal",
			want:    "operator requested accelerator",
		},
		{
			name:     "an accelerator that failed to start outranks the policy",
			policy:   "bulk build default",
			existing: "accelerator init failed",
			backend:  "cpu",
			want:     "accelerator init failed",
		},
		{
			name:    "a machine with no accelerator has nothing to report",
			policy:  "",
			backend: "cpu",
			want:    "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := writerFallbackReason(test.policy, test.existing)
			if got != test.want {
				t.Fatalf("writerFallbackReason(%q, %q) = %q, want %q",
					test.policy, test.existing, got, test.want)
			}
		})
	}
}

// The embedder carries the occasion and the lever, so the reason it records is
// the policy's own sentence rather than a constant.
func TestNativeWriterPolicyDrivesTheRecordedReason(t *testing.T) {
	native := &Native{Writer: llamacpp.Policy{Occasion: llamacpp.OccasionBulk}}
	if got := native.Writer.Occasion; got != llamacpp.OccasionBulk {
		t.Fatalf("occasion = %q, want %q", got, llamacpp.OccasionBulk)
	}
	embedder := ConfiguredEmbedder("", "", nil, nil, false,
		llamacpp.Policy{Occasion: llamacpp.OccasionBulk, Lever: llamacpp.LeverCPU})
	writer, ok := embedder.(*Native)
	if !ok {
		t.Skip("this platform does not use the native embedder")
	}
	if writer.Writer.Lever != llamacpp.LeverCPU {
		t.Fatalf("lever = %q, want %q", writer.Writer.Lever, llamacpp.LeverCPU)
	}
}
