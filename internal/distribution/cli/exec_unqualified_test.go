package cli

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestExecRefusesUnqualifiedMemoriesAndKeepsQualifiedReads(t *testing.T) {
	fixtureInstallation(t)
	runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--content", "synthetic proceso carwow marker", "--origin", "agent")

	unqualified := `SELECT content FROM memories WHERE content LIKE '%proceso carwow%'`
	err := failingRoot(t, "exec", unqualified)
	if !strings.Contains(err.Error(), `unqualified table "memories"`) ||
		!strings.Contains(err.Error(), "plugin_roca_ops.memories") ||
		!strings.Contains(err.Error(), "plugin_roca_corpus.memories") {
		t.Fatalf("unqualified exec = %v", err)
	}
	if logfile.ErrorType(err) != service.DegradedInvalidSQL {
		t.Fatalf("error_type = %q (%v)", logfile.ErrorType(err), err)
	}

	human := runRoot(t, contractBuild(), "exec",
		`SELECT content FROM plugin_roca_ops.memories WHERE content LIKE '%proceso carwow%'`)
	if !strings.Contains(human, "synthetic proceso carwow marker") {
		t.Fatalf("qualified exec lost the stored row:\n%s", human)
	}
	if _, err := runRootErr(t, contractBuild(), nil, "exec", "SELECT 1 AS n"); err != nil {
		t.Fatalf("expression SELECT = %v", err)
	}

}
