package cli

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

var allAgents = []string{"claude", "claude-desktop", "cowork", "codex", "opencode", "zcode", "pi", "hermes", "grok", "cursor"}

// init renders the full roster: every detected agent, every missing agent.
// A test that checks only a subset of the roster is a placebo — it stays
// green when an agent is silently dropped from the list.
func TestInitNamesDetectedAgents(t *testing.T) {
	t.Run("all detected", func(t *testing.T) {
		var output strings.Builder
		renderBootstrap(&cliEnv{out: &output}, service.InitResult{
			Ingest: &service.IngestResult{Result: ingest.Result{
				DetectedAgents: allAgents,
			}},
		})
		out := output.String()
		if !strings.Contains(out, "agents detected: "+strings.Join(allAgents, ", ")) {
			t.Errorf("init output does not name every detected agent:\n%s", out)
		}
		if !strings.Contains(out, "agents not found: none") {
			t.Errorf("init output should report none missing when all are detected:\n%s", out)
		}
	})

	t.Run("none detected", func(t *testing.T) {
		var output strings.Builder
		renderBootstrap(&cliEnv{out: &output}, service.InitResult{
			Ingest: &service.IngestResult{Result: ingest.Result{}},
		})
		out := output.String()
		if !strings.Contains(out, "agents detected: none") {
			t.Errorf("init output should report none detected:\n%s", out)
		}
		if !strings.Contains(out, "agents not found: "+strings.Join(allAgents, ", ")) {
			t.Errorf("init output does not name every absent agent:\n%s", out)
		}
	})
}

// doctor renders the full roster with the same semantics as init: the
// detected-agents line and the absent-agents line must each cover the full roster.
func TestDoctorNamesDetectedAgents(t *testing.T) {
	t.Run("all detected", func(t *testing.T) {
		var output strings.Builder
		renderDoctor(&cliEnv{out: &output}, service.DoctorReport{
			DetectedAgents: allAgents,
		})
		out := output.String()
		if !strings.Contains(out, "agents detected: "+strings.Join(allAgents, ", ")) {
			t.Errorf("doctor output does not name every detected agent:\n%s", out)
		}
		if !strings.Contains(out, "agents not found: none") {
			t.Errorf("doctor output should report none missing when all are detected:\n%s", out)
		}
	})

	t.Run("none detected", func(t *testing.T) {
		var output strings.Builder
		renderDoctor(&cliEnv{out: &output}, service.DoctorReport{})
		out := output.String()
		if !strings.Contains(out, "agents detected: none") {
			t.Errorf("doctor output should report none detected:\n%s", out)
		}
		if !strings.Contains(out, "agents not found: "+strings.Join(allAgents, ", ")) {
			t.Errorf("doctor output does not name every absent agent:\n%s", out)
		}
	})
}

func TestDoctorExplainsTheZeroLoginFactorySelection(t *testing.T) {
	var output strings.Builder
	renderDoctor(&cliEnv{out: &output}, service.DoctorReport{
		DetectedModelBinaries:  []string{"claude", "codex"},
		FactoryDefault:         true,
		FactoryDefaultProvider: "claude",
	})
	out := output.String()
	for _, want := range []string{
		"model binaries detected: claude, codex",
		"factory default selected: claude",
		"confirm it with roca model check claude",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output does not contain %q:\n%s", want, out)
		}
	}
}
