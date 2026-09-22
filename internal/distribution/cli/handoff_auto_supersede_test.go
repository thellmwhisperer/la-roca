package cli

import (
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

	pill := mustJSON(t, runRoot(t, contractBuild(), "exec",
		"SELECT count(*) AS n FROM plugin_roca_ops.memories WHERE layer='pill' AND json_extract(metadata, '$.pill_slug')='uso-de-la-roca' AND content LIKE '%vectors first%'",
		"--json"))
	pillRows, _ := pill["rows"].([]any)
	if len(pillRows) != 1 || pillRows[0].(map[string]any)["n"] != float64(1) {
		t.Fatalf("pill fixture = %#v, want one vector-first pill", pill["rows"])
	}
}
