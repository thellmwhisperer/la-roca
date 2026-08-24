package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
)

func TestCompactRefusesEveryReadOnlyControlBeforeOpeningTheTarget(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		flags []string
		env   string
	}{
		{name: "flag", flags: []string{"--read-only"}},
		{name: "environment", env: "1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("ROCA_READ_ONLY", testCase.env)
			target := filepath.Join(t.TempDir(), "untouched.db")
			args := append(testCase.flags, "compact", target)
			env := &cliEnv{build: Build{Version: "test"}, out: io.Discard, errOut: io.Discard}
			code, err := executeWithEnv(env, args, nil)
			if err == nil || code == ExitOK || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("compact read-only = code %d err %v", code, err)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("read-only compact touched target: %v", err)
			}
		})
	}
}

func TestCompactOutputReportsTheMeasuredVacuumFreelist(t *testing.T) {
	report := rocacorpus.CompactReport{VacuumFreelist: 0}
	var product bytes.Buffer
	if err := renderCompact(&cliEnv{out: &product}, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(product.String(), "VACUUM freelist 0") {
		t.Fatalf("compact product output = %q", product.String())
	}
	var encoded bytes.Buffer
	if err := renderCompact(&cliEnv{out: &encoded, json: true}, report); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if value, found := payload["vacuum_freelist"]; !found || value != float64(0) {
		t.Fatalf("compact JSON vacuum freelist = %v, present=%t", value, found)
	}
}
