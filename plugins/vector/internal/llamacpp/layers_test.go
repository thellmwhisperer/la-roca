package llamacpp

import (
	"runtime"
	"testing"
)

// The five occasions the issue names, decided on a machine that has an
// accelerator. Without one every answer is CPU and there is nothing to choose.
func TestPolicyDecidesByOccasionAndLever(t *testing.T) {
	tests := []struct {
		name   string
		policy Policy
		layers int
		reason string
	}{
		{
			name:   "background delta leaves the accelerator to live search",
			policy: Policy{Occasion: OccasionDelta},
			layers: 0,
			reason: "indexing leaves the accelerator for live search",
		},
		{
			name:   "readers take the accelerator",
			policy: Policy{Occasion: OccasionRead},
			layers: acceleratorLayers,
			reason: "",
		},
		{
			name:   "the lever puts a delta writer on the accelerator",
			policy: Policy{Occasion: OccasionDelta, Lever: LeverAccelerate},
			layers: acceleratorLayers,
			reason: "operator requested accelerator",
		},
		{
			name:   "a bulk build accelerates by default",
			policy: Policy{Occasion: OccasionBulk},
			layers: acceleratorLayers,
			reason: "bulk build default",
		},
		{
			name:   "the lever forces a bulk build back onto the cpu",
			policy: Policy{Occasion: OccasionBulk, Lever: LeverCPU},
			layers: 0,
			reason: "operator requested cpu",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.policy.layers(true); got != test.layers {
				t.Fatalf("gpu layers = %d, want %d", got, test.layers)
			}
			if got := test.policy.reason(true); got != test.reason {
				t.Fatalf("reason = %q, want %q", got, test.reason)
			}
		})
	}
}

// A machine without an accelerator has no decision to report: every occasion
// runs on the CPU and the log stays quiet, exactly as it does today on linux.
func TestPolicyWithoutAcceleratorStaysSilentOnTheCPU(t *testing.T) {
	for _, policy := range []Policy{
		{Occasion: OccasionRead},
		{Occasion: OccasionDelta},
		{Occasion: OccasionBulk},
		{Occasion: OccasionBulk, Lever: LeverAccelerate},
		{Occasion: OccasionDelta, Lever: LeverCPU},
	} {
		if got := policy.layers(false); got != 0 {
			t.Fatalf("%+v gpu layers = %d, want 0", policy, got)
		}
		if got := policy.reason(false); got != "" {
			t.Fatalf("%+v reason = %q, want empty", policy, got)
		}
	}
}

// The exported seat reads the platform, so the reader default keeps behaving
// the way it does today: accelerated on darwin, CPU everywhere else.
func TestGPULayersFollowsThePlatform(t *testing.T) {
	wantReader := 0
	if runtime.GOOS == "darwin" {
		wantReader = acceleratorLayers
	}
	if got := ReadPolicy().GPULayers(); got != wantReader {
		t.Fatalf("reader gpu layers = %d, want %d", got, wantReader)
	}
	if got := (Policy{Occasion: OccasionDelta}).GPULayers(); got != 0 {
		t.Fatalf("delta writer gpu layers = %d, want 0", got)
	}
}

func TestLeverFromReadsOperatorIntent(t *testing.T) {
	tests := []struct {
		value string
		want  Lever
	}{
		{"", LeverUnset},
		{"   ", LeverUnset},
		{"1", LeverAccelerate},
		{"true", LeverAccelerate},
		{" YES ", LeverAccelerate},
		{"on", LeverAccelerate},
		{"0", LeverCPU},
		{"false", LeverCPU},
		{"NO", LeverCPU},
		{"off", LeverCPU},
		{"maybe", LeverUnset},
	}
	for _, test := range tests {
		if got := LeverFrom(test.value); got != test.want {
			t.Fatalf("LeverFrom(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestLeverForTranslatesAFlag(t *testing.T) {
	if got := LeverFor(true); got != LeverAccelerate {
		t.Fatalf("LeverFor(true) = %q, want %q", got, LeverAccelerate)
	}
	if got := LeverFor(false); got != LeverCPU {
		t.Fatalf("LeverFor(false) = %q, want %q", got, LeverCPU)
	}
}

func TestWriterOccasionSeparatesDeltaFromBulk(t *testing.T) {
	if got := WriterOccasion(false); got != OccasionDelta {
		t.Fatalf("incremental occasion = %q, want %q", got, OccasionDelta)
	}
	if got := WriterOccasion(true); got != OccasionBulk {
		t.Fatalf("bulk occasion = %q, want %q", got, OccasionBulk)
	}
}
