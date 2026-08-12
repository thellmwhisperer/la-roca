package service

import (
	"strings"
	"testing"
)

func TestTruncateMarksEveryClippedEdgeWithoutDroppingTheExcerptStart(t *testing.T) {
	for _, tc := range []struct {
		name, text, term              string
		wantPrefix, wantInfix         string
		wantLeadingCut, wantSuffixCut bool
	}{
		{"tail", strings.Repeat("0123456789", 12), "", "012", "", false, true},
		{"preserved subject", "Alex, SDE: met Morgan after the synthetic launch", "Morgan", "Alex, SDE", "Morgan", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.text, 20, tc.term)
			if len([]rune(got)) != 20 {
				t.Fatalf("truncate length = %d, want 20: %q", len([]rune(got)), got)
			}
			if strings.HasPrefix(got, "…") != tc.wantLeadingCut || strings.HasSuffix(got, "…") != tc.wantSuffixCut {
				t.Fatalf("truncate markers = %q", got)
			}
			if !strings.HasPrefix(got, tc.wantPrefix) || tc.wantInfix != "" && !strings.Contains(got, tc.wantInfix) {
				t.Fatalf("truncate(%q) = %q; want prefix %q and infix %q", tc.text, got, tc.wantPrefix, tc.wantInfix)
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

func TestTruncateKeepsTinyBudgetsBounded(t *testing.T) {
	for budget := 1; budget <= 3; budget++ {
		got := truncate("Alex met Morgan after the launch", budget, "Morgan")
		if len([]rune(got)) != budget || !strings.HasSuffix(got, "…") {
			t.Errorf("budget %d produced %q", budget, got)
		}
	}
}
