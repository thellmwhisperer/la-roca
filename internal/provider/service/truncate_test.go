package service

import (
	"strings"
	"testing"
)

func TestTruncateMarksEveryClippedEdgeWithoutDroppingTheExcerptStart(t *testing.T) {
	for _, tc := range []struct {
		name, text, term       string
		wantPrefix, wantSuffix bool
	}{
		{"tail", strings.Repeat("0123456789", 12), "", false, true},
		{"both", strings.Repeat("a", 50) + "needle" + strings.Repeat("z", 50), "needle", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.text, 20, tc.term)
			if len([]rune(got)) != 20 {
				t.Fatalf("truncate length = %d, want 20: %q", len([]rune(got)), got)
			}
			if strings.HasPrefix(got, "…") != tc.wantPrefix || strings.HasSuffix(got, "…") != tc.wantSuffix {
				t.Fatalf("truncate markers = %q", got)
			}
			if tc.wantPrefix && !strings.HasPrefix(strings.TrimPrefix(got, "…"), "a") {
				t.Fatalf("relocated excerpt dropped its first rune: %q", got)
			}
		})
	}
}

func TestMatchPositionTracksUnicodeCaseChanges(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"AȺneedle", 2},
		{"Kneedle", 1},
	} {
		if got := matchPosition(tc.text, "needle"); got != tc.want {
			t.Errorf("matchPosition(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}
