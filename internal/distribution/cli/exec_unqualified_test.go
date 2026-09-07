package cli

import (
	"os"
	"os/exec"
	"path/filepath"
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

	if published := publishedRoca(t); published != "" {
		cmd := exec.Command(published, "exec", unqualified)
		cmd.Env = append(os.Environ(), "HOME="+os.Getenv("HOME"))
		out, publishedErr := cmd.CombinedOutput()
		t.Logf("published binary %s: exit_err=%v output=%s", published, publishedErr, out)
	}
}

func publishedRoca(t *testing.T) string {
	t.Helper()
	if override := strings.TrimSpace(os.Getenv("ROCA_PUBLISHED_BINARY")); override != "" {
		return override
	}
	path, err := exec.LookPath("roca")
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	t.Logf("published candidate: %s", resolved)
	return resolved
}
