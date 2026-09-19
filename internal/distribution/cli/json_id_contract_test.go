package cli

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
)

func jsonInt(t *testing.T, value any) int64 {
	t.Helper()
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			t.Fatalf("id %v: %v", value, err)
		}
		return n
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("id %q: %v", v, err)
		}
		return n
	default:
		t.Fatalf("id = %#v, want a JSON number", value)
		return 0
	}
}

func TestOpsMemoryIDsAreSQLiteAssignedAndLegacyIdsResolve(t *testing.T) {
	home := fixtureInstallation(t).home
	const historical int64 = 1152921504606853945
	insertOpsMemory(t, home, opsMemory{
		id: historical, layer: "handoff", project: "workspace",
		createdAt: "2026-01-01 00:00:00", content: "pre-migration workspace handoff",
	})
	if err := rocaops.ApplySchema(filepath.Join(home, ".roca", "plugins", "roca-ops", "roca-ops.db")); err != nil {
		t.Fatal(err)
	}

	stored := mustJSON(t, runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--project", "la-roca-e2e", "--content", "id-size probe", "--origin", "agent", "--json"))
	numeric := jsonInt(t, stored["id"])
	if numeric < 1 || numeric > 1<<53-1 {
		t.Fatalf("store --json id = %d, want a sqlite id below 2^53", numeric)
	}
	id := strconv.FormatInt(numeric, 10)

	raw := runRoot(t, contractBuild(), "exec",
		"SELECT id FROM plugin_roca_ops.memories ORDER BY created_at DESC LIMIT 1", "--json")
	if strings.Contains(raw, `"id": "`+id+`"`) {
		t.Fatalf("exec --json still wrapped the id as a string:\n%s", raw)
	}
	doc := mustJSON(t, raw)
	rows, _ := doc["rows"].([]any)
	if len(rows) == 0 {
		t.Fatalf("exec returned no rows:\n%s", raw)
	}
	first, _ := rows[0].(map[string]any)
	if jsonInt(t, first["id"]) != numeric {
		t.Fatalf("exec id = %#v, want %d", first["id"], numeric)
	}

	if _, err := exec.LookPath("node"); err == nil {
		cmd := exec.Command("node", "-e",
			`const d=JSON.parse(require("fs").readFileSync(0));const id=d.rows[0].id;if(!(Number(id)<2**53))process.exit(1);process.stdout.write(String(id))`)
		cmd.Stdin = strings.NewReader(raw)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("node parse: %v", err)
		}
		if string(out) != id {
			t.Fatalf("node parsed %q, want %q", out, id)
		}
	}

	historicalText := strconv.FormatInt(historical, 10)
	kept := mustJSON(t, runRoot(t, contractBuild(), "exec",
		"SELECT id FROM plugin_roca_ops.memories WHERE id='"+historicalText+"'", "--json"))
	keptRows, _ := kept["rows"].([]any)
	if len(keptRows) != 1 {
		t.Fatalf("historical id %s was not addressable:\n%s", historicalText, kept)
	}
	handoff := mustJSON(t, runRoot(t, contractBuild(), "handoff", "latest",
		"--project", "workspace", "--json"))
	listed, _ := handoff["handoffs"].([]any)
	if len(listed) == 0 {
		t.Fatalf("handoff latest dropped the pre-migration row:\n%s", handoff)
	}

	replacement := mustJSON(t, runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--project", "la-roca-e2e", "--content", "id-size probe 2", "--origin", "agent",
		"--supersedes", id, "--json"))
	replacementID := jsonInt(t, replacement["id"])
	if replacementID == 0 || replacementID == numeric {
		t.Fatalf("replacement = %#v, want a new memory", replacement)
	}
	pointed := mustJSON(t, runRoot(t, contractBuild(), "exec",
		"SELECT supersedes FROM plugin_roca_ops.memories WHERE id = "+strconv.FormatInt(replacementID, 10), "--json"))
	pointedRows, _ := pointed["rows"].([]any)
	if len(pointedRows) != 1 {
		t.Fatalf("supersedes %s did not land on the original row:\n%s", id, pointed)
	}
	pointedRow, _ := pointedRows[0].(map[string]any)
	if jsonInt(t, pointedRow["supersedes"]) != numeric {
		t.Fatalf("supersedes = %#v, want %d", pointedRow["supersedes"], numeric)
	}
}

func TestStoreAfterDeleteDoesNotReuseID(t *testing.T) {
	home := fixtureInstallation(t).home
	first := mustJSON(t, runRoot(t, contractBuild(), "store", "--layer", "pill",
		"--content", "disposable pill", "--origin", "agent",
		"--metadata", `{"pill_slug":"tmp-x"}`, "--json"))
	a := jsonInt(t, first["id"])
	runRoot(t, contractBuild(), "pill", "delete", "tmp-x")
	second := mustJSON(t, runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--content", "after delete", "--origin", "agent", "--json"))
	b := jsonInt(t, second["id"])
	if b <= a {
		t.Fatalf("id after delete = %d, want greater than %d", b, a)
	}
	_ = home
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
		"--metadata", `{"supersedes": "`+unsafe+`"}`, "--json"))
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

func TestStoreMetadataRejectsTrailingInput(t *testing.T) {
	fixtureInstallation(t)
	for _, metadata := range []string{
		`{"tag":"a"} {"tag":"b"}`,
		`{"tag":"a"} null`,
		`{"tag":"a"} garbage`,
	} {
		t.Run(metadata, func(t *testing.T) {
			err := failingRoot(t, "store", "--layer", "discovery",
				"--content", "invalid metadata fixture", "--origin", "agent",
				"--metadata", metadata)
			if err == nil || !strings.Contains(err.Error(), "--metadata is not a JSON object") {
				t.Fatalf("store accepted trailing metadata input or returned an unrelated error: %v", err)
			}
		})
	}
	doc := mustJSON(t, runRoot(t, contractBuild(), "exec",
		"SELECT id FROM plugin_roca_ops.memories WHERE content = 'invalid metadata fixture'", "--json"))
	rows, _ := doc["rows"].([]any)
	if len(rows) != 0 {
		t.Fatalf("store persisted invalid metadata: %v", rows)
	}
}
