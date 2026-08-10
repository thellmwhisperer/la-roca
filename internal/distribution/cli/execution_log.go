package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
)

func (env *cliEnv) logExecution(cmd *cobra.Command, started time.Time, code int, runErr error) error {
	paths, err := env.resolvePaths()
	if err != nil {
		return fmt.Errorf("resolve the execution log location: %w", err)
	}
	record := logfile.ExecutionRecord{
		Timestamp:    started.UTC(),
		Command:      commandName(cmd),
		Flags:        changedFlags(cmd),
		DatabasePath: paths.DB,
		DurationMS:   time.Since(started).Milliseconds(),
		ExitCode:     code,
		Result:       env.outcome,
	}
	if runErr != nil {
		record.Error = runErr.Error()
	}
	writer := logfile.New(filepath.Dir(paths.DB))
	if err := writer.Append(logfile.Executions, record); err != nil {
		return err
	}
	if commandName(cmd) == "ingest" && env.outcome != nil {
		return writer.Append(logfile.Ingest, logfile.IngestRecord{
			Timestamp: started.UTC(), Result: env.outcome,
		})
	}
	return nil
}

func commandName(cmd *cobra.Command) string {
	if cmd == nil {
		return "roca"
	}
	name := strings.TrimPrefix(cmd.CommandPath(), "roca ")
	if name == "roca" {
		return "root"
	}
	return name
}

func changedFlags(cmd *cobra.Command) map[string]any {
	flags := map[string]any{}
	if cmd == nil {
		return flags
	}
	visit := func(flag *pflag.Flag) {
		if !flag.Changed {
			return
		}
		value := flag.Value.String()
		if logfile.SensitiveName(flag.Name) {
			value = "[REDACTED]"
		}
		flags[flag.Name] = value
	}
	cmd.Flags().VisitAll(visit)
	cmd.InheritedFlags().VisitAll(visit)
	return flags
}
