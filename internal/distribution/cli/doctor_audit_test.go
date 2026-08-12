package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
)

func TestDoctorSurfacesRecentQueryFailuresFromBothCallSurfaces(t *testing.T) {
	fixtureInstallation(t)
	home := os.Getenv("HOME")
	now := time.Now().UTC()
	writer := logfile.New(filepath.Join(home, ".roca"))
	for _, record := range []struct {
		stream string
		value  any
	}{
		{logfile.Executions, logfile.ExecutionRecord{CallRecord: logfile.CallRecord{
			Timestamp: now.Add(-2 * time.Hour), Source: "cli", OK: false,
			Error: "synthetic invalid SQL", ErrorType: "invalid_sql", CorrelationID: "qf_cli_doctor",
		}, Command: "query"}},
		{logfile.MCPAudit, logfile.MCPRecord{CallRecord: logfile.CallRecord{
			Timestamp: now.Add(-time.Hour), Source: "mcp", OK: false,
			Error: "synthetic provider stopped", ErrorType: "model_error", CorrelationID: "qf_mcp_doctor",
		}, Tool: "roca_query"}},
	} {
		if err := writer.Append(record.stream, record.value); err != nil {
			t.Fatal(err)
		}
	}
	var out, errs strings.Builder
	code, err := execute(contractBuild(), &out, &errs, []string{"doctor"})
	if err != nil || code != ExitOK {
		t.Fatalf("doctor: code=%d err=%v stderr=%s", code, err, errs.String())
	}
	for _, want := range []string{
		"query failures (last 24h): 2", "synthetic provider stopped", "qf_mcp_doctor",
		"synthetic invalid SQL", "qf_cli_doctor",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("doctor lacks %q:\n%s", want, out.String())
		}
	}
}
