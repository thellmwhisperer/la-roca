//go:build acceptance

package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCostPlayground(t *testing.T) {
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Status string
		Forbid struct{ Paths []string }
	}
	body, err := os.ReadFile(filepath.Join(root, ".slop/dragons/S1-playground.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	if record.Status != "removed" || len(record.Forbid.Paths) == 0 {
		t.Fatal("S1 must forbid the extracted packages")
	}
	for _, name := range record.Forbid.Paths {
		matches, err := filepath.Glob(filepath.Join(root, strings.TrimSuffix(name, "/**")))
		if err != nil || len(matches) > 0 {
			t.Fatalf("S1 has a zero-byte retained-package budget: %s exists (%v)", name, err)
		}
	}
	cmd := exec.Command("go", "list", "-deps", "./cmd/roca")
	cmd.Dir = root
	deps, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range strings.Fields(string(deps)) {
		if pkg == "github.com/thellmwhisperer/la-roca/internal/provider" || strings.HasSuffix(pkg, "/sqlrepair") || strings.Contains(pkg, "/plugins/playground") {
			t.Fatalf("core imports answering package %s", pkg)
		}
	}
	m := aWorldIn(t, "playground-cost")
	after, code := m.runUnder(t, nil, "playground", "synthetic question")
	if code == 0 || !strings.Contains(after, "roca plugin install thellmwhisperer/roca-playground") {
		t.Fatalf("core without plugin: code %d, %s", code, after)
	}
	writePlaygroundEvidence(t, root, "branch", m.binary, after, false)
	if published := os.Getenv("ROCA_PUBLISHED_BIN"); published != "" {
		m = aWorldIn(t, "published-playground-cost")
		m.binary = published
		version, code := m.runUnder(t, nil, "version")
		if code != 0 || !strings.Contains(version, "v1.82.6") {
			t.Fatalf("published baseline must be v1.82.6: %s", version)
		}
		if err := m.runInit(); err != nil || m.last.code != 0 {
			t.Fatalf("published fixture init: %v %s", err, m.last.stderr)
		}
		settings := `[models]
order = ["fixture"]
[models.fixture]
command = ["/bin/sh", "-c", "printf 'SELECT 7 AS value\\n'"]
model = "fixture"
`
		if err := os.WriteFile(filepath.Join(m.home, ".roca", "config.toml"), []byte(settings), 0600); err != nil {
			t.Fatal(err)
		}
		before, code := m.runUnder(t, []string{"ROCA_MODELS_ORDER=fixture"}, "playground", "synthetic question", "--json")
		if code != 0 || !strings.Contains(before, `"path": "model"`) || !strings.Contains(before, `"value": 7`) {
			t.Fatalf("published playground did not execute: %s", before)
		}
		writePlaygroundEvidence(t, root, "published", published, before, true)
	}
}

func writePlaygroundEvidence(t *testing.T, root, label, binary, output string, wantProvider bool) {
	t.Helper()
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	hasProvider := bytes.Contains(payload, []byte("github.com/thellmwhisperer/la-roca/internal/provider."))
	if hasProvider != wantProvider {
		t.Fatalf("%s binary provider symbols=%v, want %v", label, hasProvider, wantProvider)
	}
	// Preserve the observed response while excluding fixture locations and timings.
	var response any
	if wantProvider {
		var envelope map[string]any
		if err := json.Unmarshal([]byte(output), &envelope); err != nil {
			t.Fatal(err)
		}
		for key := range envelope {
			if key == "database_path" || strings.HasSuffix(key, "_ms") {
				delete(envelope, key)
			}
		}
		response = envelope
	} else {
		response = strings.Split(strings.TrimSpace(output), " (correlation_id:")[0]
	}
	evidence := struct {
		Artifact        string `json:"artifact"`
		SHA256          string `json:"sha256"`
		ProviderSymbols bool   `json:"provider_symbols"`
		PluginInstalled bool   `json:"plugin_installed"`
		Response        any    `json:"response"`
	}{label, binaryDigest(payload), hasProvider, false, response}
	text, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".tmp", "playground-evidence")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, label+".json"), text, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(text))
	t.Log("evidence: .tmp/playground-evidence/" + label + ".json")
}

func binaryDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
