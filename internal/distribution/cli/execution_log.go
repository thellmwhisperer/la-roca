package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func (env *cliEnv) logExecution(cmd *cobra.Command, started time.Time, code int, runErr error) error {
	paths, err := env.resolvePaths()
	if err != nil {
		return fmt.Errorf("resolve the execution log location: %w", err)
	}
	operation := commandName(cmd)
	args := commandArguments(cmd)
	if env.auditCommand != "" {
		operation, args = env.auditCommand, env.auditArgs
	}
	call := logfile.CallRecord{
		Timestamp: started.UTC(), Source: "cli", Args: args, OK: code == ExitOK,
		DurationMS: time.Since(started).Milliseconds(), CorrelationID: logfile.CorrelationID(runErr),
	}
	if operation == "query" {
		call.Question = strings.Join(args, " ")
	}
	if runErr != nil {
		call.Error, call.ErrorType = runErr.Error(), logfile.ErrorType(runErr)
	}
	if env.auditQuery != nil {
		addQueryAudit(&call, *env.auditQuery)
	}
	if !call.OK && call.Error == "" {
		call.Error = fmt.Sprintf("command exited with code %d", code)
		call.ErrorType = logfile.ErrorCommandFailure
	}
	record := logfile.ExecutionRecord{
		CallRecord:   call,
		Command:      operation,
		Flags:        changedFlags(cmd),
		DatabasePath: paths.DB,
		ExitCode:     code,
		Result:       resultWithoutRows(env.outcome),
	}
	writer := logfile.New(filepath.Dir(paths.DB))
	appendRecord := writer.Append
	if env.openedDir != "" {
		appendRecord = writer.AppendExisting
	}
	if err := appendRecord(logfile.Executions, record); err != nil {
		if env.openedDir != "" && os.IsNotExist(rootError(err)) {
			return nil
		}
		return err
	}
	stream := map[string]string{"ingest": logfile.Ingest, "init": logfile.Migrations}[operation]
	if stream != "" {
		run := logfile.RunRecord{
			Timestamp: started.UTC(), OK: code == ExitOK, DurationMS: call.DurationMS,
			Error: call.Error, ErrorType: call.ErrorType, Result: env.outcome,
		}
		err := appendRecord(stream, run)
		if env.openedDir != "" && os.IsNotExist(rootError(err)) {
			return nil
		}
		return err
	}
	return nil
}

func redactPluginArguments(args []string) []string {
	redacted := append([]string(nil), args...)
	for index, argument := range redacted {
		if !strings.HasPrefix(argument, "-") {
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(argument, "-"), "=")
		if !logfile.SensitiveName(name) {
			continue
		}
		if hasValue {
			redacted[index] = argument[:strings.Index(argument, "=")+1] + "[REDACTED]"
		} else if index+1 < len(redacted) {
			redacted[index+1] = "[REDACTED]"
		}
	}
	return redacted
}

func commandArguments(cmd *cobra.Command) []string {
	if cmd == nil {
		return []string{}
	}
	args := cmd.Flags().Args()
	return append(make([]string, 0, len(args)), args...)
}

func addQueryAudit(record *logfile.CallRecord, query service.QueryResult) {
	record.Question, record.SQL = query.Question, query.CleanedSQL
	if strings.TrimSpace(query.ModelSQL) != "" && strings.TrimSpace(query.ModelSQL) != strings.TrimSpace(record.SQL) {
		record.RawSQL = query.ModelSQL
	}
	record.SQLProvider, record.SQLModel = query.Engine, query.Model
	record.RowCount, record.RetryReason = query.RowCount, query.RetryReason
	if query.QueryPlan != nil {
		record.QueryPlan = query.QueryPlan
	}
	record.ProviderNote = query.ProviderNote
	record.SQLInferenceMS = milliseconds(query.SQLInferenceMS)
	record.ExecutionMS = milliseconds(query.ExecutionMS)
	record.InterpretationProvider, record.InterpretationModel = query.InterpretEngine, query.InterpretModel
	record.InterpretationMS = milliseconds(query.InterpretationMS)
	record.FallbackReason = query.Degraded
	record.Degraded = query.Degraded
	if record.FallbackReason == "" && query.Retried {
		record.FallbackReason = "model_query_empty"
	}
	if record.OK || query.Degraded == "" {
		return
	}
	record.ErrorType = query.Degraded
	if query.ProviderError != "" {
		record.Error = query.ProviderError
	} else if query.Message != "" {
		record.Error = query.Message
	}
}

func milliseconds(value int64) *int64 { return &value }

func rootError(err error) error {
	for err != nil {
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return err
		}
		err = unwrapped
	}
	return nil
}

func resultWithoutRows(result any) any {
	if result == nil {
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return map[string]any{"summary_error": "result metadata could not be encoded"}
	}
	var summary any
	if err := json.Unmarshal(raw, &summary); err != nil {
		return map[string]any{"summary_error": "result metadata could not be encoded"}
	}
	if fields, ok := summary.(map[string]any); ok {
		delete(fields, "rows")
		delete(fields, "interpretation")
	}
	return summary
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
