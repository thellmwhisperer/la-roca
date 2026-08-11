package config_test

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

// Read-only mode is the operator's switch, not a state the product infers. It
// is read from the environment because it has to hold for a single command as
// easily as for a whole shell.
func TestReadOnlyIsOffUnlessTheOperatorTurnsItOn(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"no", false},
		{"off", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"on", true},
	}
	for _, tc := range cases {
		t.Run("ROCA_READ_ONLY="+tc.value, func(t *testing.T) {
			if got := config.ReadOnly(tc.value); got != tc.want {
				t.Errorf("ReadOnly(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// A value nobody can read as a yes or a no is treated as a yes: the operator
// wrote the variable meaning something by it, and guessing "off" would let a
// typo silently license the writes they were trying to forbid.
func TestReadOnlyTreatsAnUnreadableValueAsOn(t *testing.T) {
	if !config.ReadOnly("solo-lectura") {
		t.Error("an unreadable value licensed the writes it was written to forbid")
	}
}
