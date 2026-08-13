// Package mcpplug is La Roca's MCP surface: the plug, not the product.
//
// The CLI is the complete surface over the kernel. This package exists for the
// agents that have no shell, and it carries exactly five tools, each one a
// single call into the same service object `roca query` drives. There is no
// state between calls: the process is born when the agent launches it and dies
// when the agent closes the pipe, matching the stateless protocol.
//
// The law of this package is pinned by passthrough_test.go and not by this
// comment: a handler with logic of its own is a capability no other surface
// can reach.
package mcpplug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// ServerName is how La Roca announces itself in the handshake, and it is the
// same name the entry in an agent's configuration carries.
const ServerName = "roca"

// instructions is what a client reads before choosing a tool. It says what this
// server is for and what it is not, because the alternative is an agent calling
// `roca_sql` to answer a question `roca_query` answers whole.
const instructions = "La Roca is local semantic memory for agent fleets: it answers " +
	"natural-language questions about what the agents on this machine have left " +
	"behind. Ask with roca_query. Use roca_sql only to compile SQL, then roca_exec " +
	"to run it under the read-only gate. Write back what is worth remembering with roca_store."

// Build is what the linker put inside the binary. The version the handshake
// declares is the product's, never the SDK's: an agent that reports a library
// version has told you nothing about what answered it.
type Build struct {
	Version string
	Commit  string
}

// plug is the service, held so that every handler is a method with one call in
// it.
type plug struct{ svc *service.Service }

// New builds the server with the five tools of the decided surface, in the
// order they are declared here.
//
// Every handler is wrapped in sanitizing so that an MCP tool error never carries
// the database file path. No agent surface reveals where the database lives on
// disk. The handlers
// themselves stay one-statement pass-throughs; the sanitization lives here, in
// the glue between the handlers and the SDK, where it cannot grow logic the
// other surfaces cannot reach.
func New(svc *service.Service, build Build) *mcp.Server {
	p := &plug{svc: svc}
	server := mcp.NewServer(&mcp.Implementation{
		Name:        ServerName,
		Title:       "La Roca",
		Description: "Local semantic memory for agent fleets",
		Version:     build.Version,
	}, &mcp.ServerOptions{Instructions: instructions})

	dbPath := svc.DB().Path()
	dataDir := svc.DataDir()
	audit := logfile.New(svc.DataDir())
	_ = audit.Prepare()
	mcp.AddTool(server, execTool, sanitizing(p.exec, dbPath, dataDir))
	mcp.AddTool(server, healthTool, sanitizing(p.health, dbPath, dataDir))
	mcp.AddTool(server, queryTool, sanitizing(p.query, dbPath, dataDir))
	mcp.AddTool(server, sqlTool, sanitizing(p.sql, dbPath, dataDir))
	mcp.AddTool(server, storeTool, sanitizing(p.store, dbPath, dataDir))
	server.AddReceivingMiddleware(auditCalls(audit, os.Stderr))
	return server
}

// sanitizing wraps a tool handler so that its error never carries the database
// file path. The handler is called first; the error it returns is scrubbed of
// every occurrence of dbPath, replaced with "the database", and the rest of the
// result passes through untouched.
func sanitizing[In, Out any](
	h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error),
	dbPath, dataDir string,
) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		res, out, err := h(ctx, req, in)
		if res != nil {
			for _, content := range res.Content {
				if text, ok := content.(*mcp.TextContent); ok {
					text.Text = scrubDataDir(text.Text, dataDir)
				}
			}
		}
		return res, out, ScrubPath(err, dbPath)
	}
}

func auditCalls(audit *logfile.Writer, warnings io.Writer) mcp.Middleware {
	var warned sync.Once
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			started := time.Now()
			tool, args := toolCall(req)
			result, err := next(ctx, method, req)
			ok := err == nil
			// What the client reads as an error is what needs an ID in it. A
			// degraded answer this surface marks IsError is one of them, and it
			// reaches the agent as an error like any other.
			surfaced := err != nil
			if callResult, isToolResult := result.(*mcp.CallToolResult); isToolResult && callResult.IsError {
				ok, surfaced = false, true
			}
			degraded := resultDegraded(result)
			ok = ok && degraded == ""
			call := logfile.CallRecord{
				Timestamp: started.UTC(), Source: "mcp", Args: args, OK: ok,
				DurationMS: time.Since(started).Milliseconds(), Question: metaValue[string](result, "question"),
				SQL: metaValue[string](result, "sql"), RawSQL: metaValue[string](result, "raw_sql"),
				SQLProvider: metaValue[string](result, "sql_provider"), SQLModel: metaValue[string](result, "sql_model"),
				RowCount: resultRows(result), FallbackReason: metaValue[string](result, "fallback_reason"),
				Degraded:    degraded,
				RetryReason: metaValue[string](result, "retry_reason"), QueryPlan: metaValue[any](result, "queryplan"),
				Path: metaValue[string](result, "path"), Retried: metaValue[bool](result, "retried"),
				RetriedSQL: metaValue[bool](result, "retried_sql"), RetryType: metaValue[string](result, "retry_type"),
				FirstModelSQL:             metaValue[string](result, "first_model_sql"),
				ProviderNote:              metaValue[string](result, "sql_provider_note"),
				SQLProviderLatencyMS:      resultMilliseconds(result, "sql_provider_latency_ms"),
				SQLInferenceMS:            resultMilliseconds(result, "sql_inference_ms"),
				SQLRetryProviderLatencyMS: resultMilliseconds(result, "sql_retry_provider_latency_ms"),
				SQLRetryInferenceMS:       resultMilliseconds(result, "sql_retry_inference_ms"),
				ExecutionMS:               resultMilliseconds(result, "execution_ms"),
				InterpretationProvider:    metaValue[string](result, "interpretation_provider"),
				InterpretationModel:       metaValue[string](result, "interpretation_model"),
				InterpretationMS:          resultMilliseconds(result, "interpretation_ms"),
			}
			if call.Question == "" && (tool == "roca_query" || tool == "roca_sql") {
				call.Question = argumentString(args, "query")
			}
			if tool == "roca_query" || tool == "roca_sql" {
				modelSQL := metaValue[string](result, "model_sql")
				call.ModelSQL = &modelSQL
			}
			if call.SQL == "" && tool == "roca_exec" {
				call.SQL = argumentString(args, "sql")
			}
			if !ok {
				call.Error, call.ErrorType = metaValue[string](result, "error"), metaValue[string](result, "error_type")
				if err != nil {
					call.Error, call.ErrorType = err.Error(), logfile.ErrorType(err)
				} else {
					if call.Error == "" {
						call.Error = resultErrorText(result)
					}
					if call.ErrorType == "" {
						call.ErrorType = degraded
					}
					if call.ErrorType == "" {
						call.ErrorType = logfile.ErrorToolError
					}
				}
				if surfaced {
					call.CorrelationID = logfile.NewCorrelationID()
				}
			}
			if appendErr := audit.AppendExisting(logfile.MCPAudit, logfile.MCPRecord{
				CallRecord: call, Tool: tool,
			}); appendErr != nil {
				warned.Do(func() {
					fmt.Fprintf(warnings, "warning: MCP calls are not being written to the audit log: %v\n", appendErr)
				})
			} else if call.CorrelationID != "" {
				if err != nil {
					err = fmt.Errorf("%w (correlation_id: %s)", err, call.CorrelationID)
				} else {
					appendResultCorrelation(result, call.CorrelationID)
				}
			}
			return result, err
		}
	}
}

func argumentString(value any, key string) string {
	fields, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	text, _ := fields[key].(string)
	return text
}

func resultErrorText(value any) string {
	result, ok := value.(*mcp.CallToolResult)
	if !ok {
		return "tool call failed"
	}
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok && strings.TrimSpace(text.Text) != "" {
			return text.Text
		}
	}
	return "tool call failed"
}

func appendResultCorrelation(value any, correlationID string) {
	result, ok := value.(*mcp.CallToolResult)
	if !ok {
		return
	}
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			text.Text = strings.TrimRight(text.Text, "\n") + "\ncorrelation_id: " + correlationID
			return
		}
	}
}

// metaValue reads one call metadata entry of the asked type, yielding the zero
// value when the result carries no metadata or the entry is absent or of
// another type.
func metaValue[T any](value any, key string) T {
	switch result := value.(type) {
	case *mcp.CallToolResult:
		return metaValue[T](result.Meta, key)
	case mcp.Meta:
		return metaValue[T](map[string]any(result), key)
	case map[string]any:
		typed, _ := result[key].(T)
		return typed
	}
	var zero T
	return zero
}

func resultMilliseconds(value any, key string) *int64 {
	var milliseconds int64
	switch result := value.(type) {
	case *mcp.CallToolResult:
		return resultMilliseconds(result.Meta, key)
	case mcp.Meta:
		value, exists := result[key]
		if !exists {
			return nil
		}
		milliseconds = numberAsInt64(value)
	case map[string]any:
		value, exists := result[key]
		if !exists {
			return nil
		}
		milliseconds = numberAsInt64(value)
	default:
		return nil
	}
	return &milliseconds
}

func numberAsInt64(value any) int64 {
	switch number := value.(type) {
	case int64:
		return number
	case int:
		return int64(number)
	case float64:
		return int64(number)
	default:
		return 0
	}
}

func resultDegraded(value any) string {
	switch result := value.(type) {
	case *mcp.CallToolResult:
		return resultDegraded(result.Meta)
	case service.QueryResult:
		return result.Degraded
	case mcp.Meta:
		degraded, _ := result["degraded"].(string)
		return degraded
	case map[string]any:
		degraded, _ := result["degraded"].(string)
		return degraded
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(result, &decoded) == nil {
			return resultDegraded(decoded)
		}
	}
	return ""
}

func toolCall(req mcp.Request) (string, any) {
	call, ok := req.(*mcp.CallToolRequest)
	if !ok || call.Params == nil {
		return "unknown", map[string]any{}
	}
	args := any(map[string]any{})
	if len(call.Params.Arguments) > 0 {
		if err := json.Unmarshal(call.Params.Arguments, &args); err != nil {
			args = string(call.Params.Arguments)
		}
	}
	return call.Params.Name, args
}

func resultRows(value any) int {
	switch result := value.(type) {
	case *mcp.CallToolResult:
		return resultRows(result.Meta)
	case service.QueryResult:
		return result.RowCount
	case service.ExecResult:
		return result.RowCount
	case service.StoreResult:
		if result.ID != 0 {
			return 1
		}
	case service.HealthReport:
		rows := 0
		for _, check := range result.Checks {
			rows += len(check.Rows)
		}
		return rows
	case mcp.Meta:
		return metaRowCount(result["row_count"])
	case map[string]any:
		return metaRowCount(result["row_count"])
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(result, &decoded) == nil {
			return resultRows(decoded)
		}
	}
	return 0
}

func metaRowCount(value any) int {
	switch count := value.(type) {
	case int:
		return count
	case float64:
		return int(count)
	default:
		return 0
	}
}

// rendered paints the AXI TOON text for a service answer and returns no typed
// output value. The SDK serializes every non-nil typed output into
// StructuredContent, which makes clients prefer a raw JSON envelope over the
// compact text. Returning any(nil) omits both that envelope and the inferred
// output schema. Metadata carries only row-free audit fields; it never carries
// columns, rows or cell data.
func rendered[T any](res T, err error, paint func(T) string) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	metadata := mcp.Meta{
		"row_count": resultRows(res),
		"degraded":  resultDegraded(res),
	}
	if query, ok := any(res).(service.QueryResult); ok {
		metadata["path"] = query.Path
		metadata["retried"] = query.Retried
		metadata["retried_sql"] = query.RetriedSQL
		metadata["model_sql"] = query.ModelSQL
		metadata["first_model_sql"] = query.FirstModelSQL
		metadata["question"] = query.Question
		metadata["sql"] = query.CleanedSQL
		if strings.TrimSpace(query.ModelSQL) != "" && strings.TrimSpace(query.ModelSQL) != strings.TrimSpace(query.CleanedSQL) {
			metadata["raw_sql"] = query.ModelSQL
		}
		metadata["sql_provider"] = query.Engine
		metadata["sql_model"] = query.Model
		metadata["sql_provider_latency_ms"] = query.LLMLatencyMS
		metadata["sql_inference_ms"] = query.SQLInferenceMS
		metadata["sql_retry_inference_ms"] = query.SQLRetryInferenceMS
		metadata["sql_retry_provider_latency_ms"] = query.SQLRetryProviderLatencyMS
		metadata["execution_ms"] = query.ExecutionMS
		metadata["interpretation_provider"] = query.InterpretEngine
		metadata["interpretation_model"] = query.InterpretModel
		metadata["interpretation_ms"] = query.InterpretationMS
		metadata["retry_reason"] = query.RetryReason
		metadata["retry_type"] = query.RetryType
		if query.ClarificationRequired {
			metadata["clarification_required"] = true
			metadata["missing_slot"] = query.MissingSlot
		}
		if query.QueryPlan != nil {
			metadata["queryplan"] = query.QueryPlan
		}
		metadata["sql_provider_note"] = query.ProviderNote
		fallback := query.Degraded
		if fallback == "" && query.Retried {
			fallback = "model_query_empty"
		}
		metadata["fallback_reason"] = fallback
		if query.Degraded != "" {
			metadata["error_type"] = query.Degraded
			metadata["error"] = query.ProviderError
			if metadata["error"] == "" {
				metadata["error"] = query.Message
			}
		}
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: paint(res)}},
		Meta:    metadata,
	}, nil, nil
}

// queryText shapes a natural-language answer. The query tool runs the question
// and the sql tool compiles it without running; both produce a QueryResult, and
// axi.Query shows the rows for one and the SQL for the other, so they share a
// wrapper.
func queryText(res service.QueryResult, err error) (*mcp.CallToolResult, any, error) {
	result, _, callErr := rendered(res, err, axi.MCPQuery)
	if result != nil {
		result.IsError = service.IsDegradedFailure(res.Degraded)
	}
	return result, nil, callErr
}

func execText(res service.ExecResult, err error) (*mcp.CallToolResult, any, error) {
	return rendered(res, err, axi.MCPExec)
}

func storeText(res service.StoreResult, err error) (*mcp.CallToolResult, any, error) {
	return rendered(res, err, axi.Store)
}

func healthText(res service.HealthReport, err error) (*mcp.CallToolResult, any, error) {
	return rendered(res, err, axi.Health)
}

// ScrubPath replaces every occurrence of the database path in an error message
// with "the database", so that MCP agents never see the filesystem location.
func ScrubPath(err error, dbPath string) error {
	if err == nil || dbPath == "" {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, dbPath) {
		return err
	}
	// Only Error() reaches the agent, so the scrubbed text is what it reads. The
	// original stays as the unwrap target because rebuilding the error threw away
	// the declared category and the audit line then said unclassified_error.
	return scrubbedError{err: err, message: strings.ReplaceAll(msg, dbPath, "the database")}
}

type scrubbedError struct {
	err     error
	message string
}

func (e scrubbedError) Error() string { return e.message }

func (e scrubbedError) Unwrap() error { return e.err }

func scrubDataDir(text, dataDir string) string {
	if dataDir == "" {
		return text
	}
	return strings.ReplaceAll(text, dataDir, "the data directory")
}

// Serve runs the server over stdio in the foreground until the client closes
// the pipe. There is no daemon: this process is the session.
func Serve(ctx context.Context, svc *service.Service, build Build) error {
	return serveOver(ctx, svc, build, &mcp.StdioTransport{})
}

// serveOver is Serve with the transport injected, which is what lets a test
// drive the same code path over a pipe it owns.
func serveOver(ctx context.Context, svc *service.Service, build Build,
	transport mcp.Transport) error {
	err := New(svc, build).Run(ctx, transport)
	// A closed pipe is how this server ends its life, not a failure: the agent
	// that launched it went away, which is exactly the contract. The comparison
	// is errors.Is because the transport wraps: an == check reported the normal
	// end of life as a failure the moment the error arrived wrapped.
	if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}
