//go:build acceptance

package acceptance

import (
	"strings"
	"testing"
)

func TestWordSearchSkipsMemoriesThatAreNotCurrent(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatal(err)
	}
	home, world := initializedDistributionHome(t, "roca-word-search-current-", binary)
	for _, args := range [][]string{
		{"--content", "zircon lantern resolved", "--status", "resolved"},
		{"--content", "zircon lantern expired", "--metadata", `{"expires_at":"2020-01-01T00:00:00Z"}`},
		{"--content", "zircon lantern current"},
	} {
		if run := world.runAt(home, binary, append([]string{"store", "--layer", "discovery"}, args...)...); run.code != 0 {
			t.Fatalf("store %v: %+v", args, run)
		}
	}
	run := world.runAt(home, binary, "query", "zircon lantern")
	if run.code != 0 || !strings.Contains(run.stdout, "zircon lantern current") ||
		strings.Contains(run.stdout, "zircon lantern resolved") || strings.Contains(run.stdout, "zircon lantern expired") {
		t.Fatalf("word search answers only the current memory: %+v", run)
	}
}
