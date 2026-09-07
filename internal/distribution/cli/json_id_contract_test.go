package cli

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestOpsMemoryIDsAreDecimalStringsUnderJSON(t *testing.T) {
	fixtureInstallation(t)

	stored := mustJSON(t, runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--content", "ops identifier fixture", "--origin", "agent", "--json"))
	id, ok := stored["id"].(string)
	if !ok || id == "" {
		t.Fatalf("store --json id = %#v, want a decimal string", stored["id"])
	}
	numeric, err := strconv.ParseInt(id, 10, 64)
	if err != nil || numeric <= 1<<53 {
		t.Fatalf("store --json id = %q, want an ops id above 2^53", id)
	}

	raw := runRoot(t, contractBuild(), "exec",
		"SELECT id FROM plugin_roca_ops.memories LIMIT 1", "--json")
	if strings.Contains(raw, `"id": `+id) && !strings.Contains(raw, `"id": "`+id) {
		t.Fatalf("exec --json still emitted a JSON number:\n%s", raw)
	}
	doc := mustJSON(t, raw)
	rows, _ := doc["rows"].([]any)
	if len(rows) == 0 {
		t.Fatalf("exec returned no rows:\n%s", raw)
	}
	first, _ := rows[0].(map[string]any)
	if first["id"] != id {
		t.Fatalf("exec id = %#v, want %q", first["id"], id)
	}

	if _, err := exec.LookPath("node"); err == nil {
		cmd := exec.Command("node", "-e",
			`const j=JSON.parse(require("fs").readFileSync(0,"utf8")); process.stdout.write(String(j.rows[0].id))`)
		cmd.Stdin = strings.NewReader(raw)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("node parse: %v", err)
		}
		if string(out) != id {
			t.Fatalf("node parsed %q, want the exact id %q", out, id)
		}
	}

	replacement := mustJSON(t, runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--content", "ops identifier replacement", "--origin", "agent",
		"--supersedes", id, "--json"))
	if replacement["id"] == nil || replacement["id"] == id {
		t.Fatalf("replacement = %#v, want a new memory", replacement)
	}
	pointed := mustJSON(t, runRoot(t, contractBuild(), "exec",
		"SELECT id FROM plugin_roca_ops.memories WHERE supersedes = "+id, "--json"))
	pointedRows, _ := pointed["rows"].([]any)
	if len(pointedRows) != 1 {
		t.Fatalf("supersedes %s did not land on the original row:\n%s", id, pointed)
	}
}

func TestStoreSupersedesStillAcceptsTheNumericForm(t *testing.T) {
	fixtureInstallation(t)
	err := failingRoot(t, "store", "--layer", "discovery",
		"--content", "points at a missing numeric id", "--supersedes", "42")
	if err == nil || !strings.Contains(err.Error(), "42") {
		t.Fatalf("numeric --supersedes was not accepted and refused by identity: %v", err)
	}
}

func TestStoreMetadataKeepsAnUnsafeIdInsideTheBlob(t *testing.T) {
	fixtureInstallation(t)
	const unsafe = "1152921504606853875"
	stored := mustJSON(t, runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--content", "metadata identifier fixture", "--origin", "agent",
		"--metadata", `{"supersedes": `+unsafe+`}`, "--json"))
	if stored["id"] == nil {
		t.Fatalf("store --json lost its id: %v", stored)
	}
	raw := runRoot(t, contractBuild(), "exec",
		"SELECT metadata FROM plugin_roca_ops.memories WHERE content = 'metadata identifier fixture'",
		"--json")
	doc := mustJSON(t, raw)
	rows, _ := doc["rows"].([]any)
	if len(rows) == 0 {
		t.Fatalf("exec returned no metadata row:\n%s", raw)
	}
	first, _ := rows[0].(map[string]any)
	blob, _ := first["metadata"].(string)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(blob), &parsed); err != nil {
		t.Fatalf("metadata %q is not JSON: %v", blob, err)
	}
	if parsed["supersedes"] != unsafe {
		t.Fatalf("metadata supersedes = %#v, want %q", parsed["supersedes"], unsafe)
	}
}
