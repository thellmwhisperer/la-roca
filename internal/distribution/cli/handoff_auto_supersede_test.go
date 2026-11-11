package cli

import (
	"slices"
	"testing"
)

func TestStoreHandoffAutoSupersedesPreviousCurrentForTheProject(t *testing.T) {
	fixture := fixtureInstallation(t)
	const project = "la-roca-e2e-handoff-auto"
	insertOpsMemory(t, fixture.home, opsMemory{
		layer: "pill", content: "uso-de-la-roca: vectors first, then qualified exec",
		metadata: map[string]any{"pill_slug": "uso-de-la-roca"},
	})
	insertOpsMemory(t, fixture.home, opsMemory{
		id: 7205, layer: "handoff", project: project,
		content:   "branch/scope: fixture\ndone: handoff 7205\nstate: current\nnext: use the vector-first method",
		createdAt: "2026-09-21 12:17:00",
	})

	first := mustJSON(t, runRoot(t, contractBuild(), "store",
		"--layer", "handoff", "--project", project,
		"--content", "branch/scope: fixture\ndone: handoff A\nstate: current\nnext: continue with the vector-first method",
		"--agent", "claude", "--model", "sonnet", "--json"))
	firstID := jsonInt(t, first["id"])
	if firstID == 0 {
		t.Fatalf("first store id = %#v", first["id"])
	}

	second := mustJSON(t, runRoot(t, contractBuild(), "store",
		"--layer", "handoff", "--project", project,
		"--content", "handoff auto B",
		"--agent", "claude", "--model", "sonnet", "--json"))
	secondID := jsonInt(t, second["id"])
	if secondID == 0 || secondID == firstID {
		t.Fatalf("second store = %#v, first=%d", second["id"], firstID)
	}

	latest := mustJSON(t, runRoot(t, contractBuild(),
		"handoff", "latest", "--project", project, "--json"))
	handoffs, _ := latest["handoffs"].([]any)
	if len(handoffs) != 1 {
		t.Fatalf("handoff latest = %#v, want exactly one current", latest["handoffs"])
	}
	got, _ := handoffs[0].(map[string]any)
	if jsonInt(t, got["id"]) != secondID {
		t.Fatalf("latest id = %#v, want %d", got["id"], secondID)
	}

	counted := mustJSON(t, runRoot(t, contractBuild(), "exec",
		"SELECT count(*) AS n FROM plugin_roca_ops.memories WHERE project='la-roca-e2e-handoff-auto' AND layer='handoff' AND id NOT IN (SELECT supersedes FROM plugin_roca_ops.memories WHERE supersedes IS NOT NULL)",
		"--json"))
	rows, _ := counted["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("exec count rows = %#v", counted["rows"])
	}
	row, _ := rows[0].(map[string]any)
	switch n := row["n"].(type) {
	case float64:
		if n != 1 {
			t.Fatalf("non-superseded count = %v, want exactly 1", n)
		}
	default:
		t.Fatalf("count n = %#v", row["n"])
	}
}

func TestOnlyAHandoffOfTheSameProjectRetiresTheCurrentHandoff(t *testing.T) {
	fixture := fixtureInstallation(t)
	const project = "la-roca-e2e-handoff-retire"
	insertOpsMemory(t, fixture.home, opsMemory{
		id: 7300, layer: "handoff", project: project,
		content:   "branch/scope: fixture\ndone: handoff 7300\nstate: current\nnext: continue",
		createdAt: "2026-09-21 12:00:00",
	})
	latestIDs := func() []int64 {
		t.Helper()
		latest := mustJSON(t, runRoot(t, contractBuild(),
			"handoff", "latest", "--project", project, "--json"))
		handoffs, _ := latest["handoffs"].([]any)
		var ids []int64
		for _, handoff := range handoffs {
			row, _ := handoff.(map[string]any)
			ids = append(ids, jsonInt(t, row["id"]))
		}
		return ids
	}
	store := func(args ...string) int64 {
		t.Helper()
		args = append([]string{"store", "--agent", "claude", "--model", "sonnet", "--json"}, args...)
		return jsonInt(t, mustJSON(t, runRoot(t, contractBuild(), args...))["id"])
	}

	store("--layer", "discovery", "--project", project, "--supersedes", "7300",
		"--content", "a discovery that names the handoff")
	if got := latestIDs(); !slices.Equal(got, []int64{7300}) {
		t.Fatalf("after a discovery supersedes 7300, handoff latest = %v, want [7300]", got)
	}

	store("--layer", "handoff", "--project", "la-roca-e2e-other-project", "--supersedes", "7300",
		"--content", "branch/scope: other\ndone: other\nstate: other\nnext: other")
	if got := latestIDs(); !slices.Equal(got, []int64{7300}) {
		t.Fatalf("after another project's handoff supersedes 7300, handoff latest = %v, want [7300]", got)
	}

	// An older parallel head that only a discovery names is still a head: the
	// next handoff of the project retires it too.
	insertOpsMemory(t, fixture.home, opsMemory{
		id: 7299, layer: "handoff", project: project,
		content:   "branch/scope: fixture\ndone: handoff 7299\nstate: parallel\nnext: continue",
		createdAt: "2026-09-21 11:00:00",
	})
	store("--layer", "discovery", "--project", project, "--supersedes", "7299",
		"--content", "a discovery that names the parallel handoff")
	next := store("--layer", "handoff", "--project", project,
		"--content", "branch/scope: fixture\ndone: next\nstate: current\nnext: continue")
	if got := latestIDs(); !slices.Equal(got, []int64{next}) {
		t.Fatalf("after the project's next handoff, handoff latest = %v, want [%d]", got, next)
	}
}
