package cli

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestOpsMemoryIDsAreDecimalStringsUnderJSON(t *testing.T) {
	home := fixtureInstallation(t).home
	const historical int64 = 1152921504606853945
	insertOpsMemory(t, home, opsMemory{
		id: historical, layer: "handoff", project: "workspace",
		createdAt: "2026-01-01 00:00:00", content: "pre-migration workspace handoff",
	})

	stored := mustJSON(t, runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--project", "la-roca-e2e", "--content", "id-size probe", "--origin", "agent", "--json"))
	id, ok := stored["id"].(string)
	if !ok || id == "" {
		t.Fatalf("store --json id = %#v, want a decimal string", stored["id"])
	}
	numeric, err := strconv.ParseInt(id, 10, 64)
	if err != nil || numeric >= 1<<53 || len(id) > 12 {
		t.Fatalf("store --json id = %q, want a short ops id below 2^53 and at most 12 digits", id)
	}

	raw := runRoot(t, contractBuild(), "exec",
		"SELECT id FROM plugin_roca_ops.memories ORDER BY created_at DESC LIMIT 1", "--json")
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
			`const d=JSON.parse(require("fs").readFileSync(0));const id=d.rows[0].id;if(!(Number(id)<2**53))process.exit(1);process.stdout.write(String(id))`)
		cmd.Stdin = strings.NewReader(raw)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("node parse: %v", err)
		}
		if string(out) != id {
			t.Fatalf("node parsed %q, want the exact id %q", out, id)
		}
	}

	historicalText := strconv.FormatInt(historical, 10)
	kept := mustJSON(t, runRoot(t, contractBuild(), "exec",
		"SELECT id FROM plugin_roca_ops.memories WHERE id='"+historicalText+"'", "--json"))
	keptRows, _ := kept["rows"].([]any)
	if len(keptRows) != 1 {
		t.Fatalf("historical id %s was not addressable:\n%s", historicalText, kept)
	}
	keptRow, _ := keptRows[0].(map[string]any)
	if keptRow["id"] != historicalText {
		t.Fatalf("historical exec id = %#v, want %q", keptRow["id"], historicalText)
	}
	handoff := mustJSON(t, runRoot(t, contractBuild(), "handoff", "latest",
		"--project", "workspace", "--json"))
	listed, _ := handoff["handoffs"].([]any)
	foundHistorical := false
	for _, item := range listed {
		row, _ := item.(map[string]any)
		if row["id"] == historicalText {
			foundHistorical = true
			break
		}
	}
	if !foundHistorical {
		t.Fatalf("handoff latest dropped the pre-migration row:\n%s", handoff)
	}

	replacement := mustJSON(t, runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--project", "la-roca-e2e", "--content", "id-size probe 2", "--origin", "agent",
		"--supersedes", id, "--json"))
	replacementID, _ := replacement["id"].(string)
	if replacementID == "" || replacementID == id {
		t.Fatalf("replacement = %#v, want a new memory", replacement)
	}
	pointed := mustJSON(t, runRoot(t, contractBuild(), "exec",
		"SELECT supersedes FROM plugin_roca_ops.memories WHERE id = '"+replacementID+"'", "--json"))
	pointedRows, _ := pointed["rows"].([]any)
	if len(pointedRows) != 1 {
		t.Fatalf("supersedes %s did not land on the original row:\n%s", id, pointed)
	}
	pointedRow, _ := pointedRows[0].(map[string]any)
	if pointedRow["supersedes"] != id {
		t.Fatalf("supersedes = %#v, want %q", pointedRow["supersedes"], id)
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
		"--metadata", `{"supersedes": `+unsafe+`} `+"\n\t", "--json"))
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
