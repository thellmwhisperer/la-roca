package cli

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestIngestExitsNonZeroWhenAWriteFails(t *testing.T) {
	env := &cliEnv{}
	applyIngestExitCode(env, service.IngestResult{Result: ingest.Result{WriteFailed: 1, Errors: 1}})
	if env.code != ExitError {
		t.Fatalf("code = %d, want %d", env.code, ExitError)
	}
}

func TestIngestKeepsSuccessWhenOnlyAReadFailed(t *testing.T) {
	env := &cliEnv{}
	applyIngestExitCode(env, service.IngestResult{Result: ingest.Result{Errors: 1}})
	if env.code != ExitOK {
		t.Fatalf("code = %d, want %d", env.code, ExitOK)
	}
}
