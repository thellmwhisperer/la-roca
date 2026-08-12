package mcpplug_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/mcpplug"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// The surface this version decided, and no other. `roca_list_runs` is out
// because `runs` is v2: a tool with no table behind it is a tool that lies.
var theDecidedSurface = []string{
	"roca_exec", "roca_health", "roca_query", "roca_sql", "roca_store",
}

// The tools the pruning withdrew. They are named here so that reintroducing one
// through the back door is a red test and not a code review nobody ran.
var theWithdrawnTools = []string{
	"roca_list_runs", "roca_inbox", "roca_proposals", "roca_video", "roca_vision",
}

func TestDiscoveryReturnsExactlyTheDecidedSurface(t *testing.T) {
	session := connect(t, seededService(t))
	tools := listTools(t, session)
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" {
			t.Errorf("tool %q has no description: an agent cannot choose it", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tool.Name)
		}
		if tool.OutputSchema != nil {
			t.Errorf("tool %q advertises structured output; AXI TOON is the only answer", tool.Name)
		}
	}
	if !reflect.DeepEqual(names, theDecidedSurface) {
		t.Errorf("tools = %v, want %v", names, theDecidedSurface)
	}
}

func TestTheExecToolTakesOnlySQLAndTheCLIsTextBudget(t *testing.T) {
	session := connect(t, seededService(t))
	tools := listTools(t, session)

	for _, tool := range tools.Tools {
		if tool.Name != "roca_exec" {
			continue
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("exec schema = %T, want an object", tool.InputSchema)
		}
		properties, _ := schema["properties"].(map[string]any)
		if got := sortedKeys(properties); !reflect.DeepEqual(got, []string{"max_chars", "sql"}) {
			t.Errorf("exec arguments = %v, want only sql and max_chars", got)
		}
		if required, _ := schema["required"].([]any); !reflect.DeepEqual(required, []any{"sql"}) {
			t.Errorf("required exec arguments = %v, want only sql", required)
		}
		if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
			t.Errorf("additionalProperties = %v, want false", schema["additionalProperties"])
		}
		maxChars, _ := properties["max_chars"].(map[string]any)
		if strings.Contains(fmt.Sprint(maxChars["description"]), "default=") {
			t.Errorf("schema advertises a description-only default: %v", maxChars)
		}
		return
	}
	t.Fatal("roca_exec is not listed")
}

func TestNoWithdrawnToolComesBackThroughTheBackDoor(t *testing.T) {
	session := connect(t, seededService(t))
	tools := listTools(t, session)
	for _, tool := range tools.Tools {
		for _, withdrawn := range theWithdrawnTools {
			if tool.Name == withdrawn {
				t.Errorf("the withdrawn tool %q is published again", withdrawn)
			}
		}
	}
}

// Parity is the plug's whole contract: the same question over the two surfaces
// is the same answer, because there is one object behind both.
func TestTheSameQuestionThroughThePlugAndThroughTheServiceIsTheSameAnswer(t *testing.T) {
	svc := seededService(t)
	session := connect(t, svc)
	ctx := context.Background()

	direct, err := svc.Query(ctx, service.QueryRequest{
		Question: "how many memories are there",
	})
	if err != nil {
		t.Fatalf("Query over the service: %v", err)
	}
	throughThePlug := queryThroughThePlug(t, session, map[string]any{
		"query": "how many memories are there",
	})

	wantRows := axi.RowOutput(direct.Columns, direct.Rows, direct.Question)
	if !strings.Contains(throughThePlug, wantRows) {
		t.Errorf("plug answer lacks the service rows:\n%s\nwant rows:\n%s", throughThePlug, wantRows)
	}
	if direct.Path == service.PathUnresolved {
		if throughThePlug != direct.Message {
			t.Errorf("plug unresolved answer = %q, service = %q", throughThePlug, direct.Message)
		}
	} else if !strings.Contains(throughThePlug, "route "+string(direct.Path)) {
		t.Errorf("plug answer lacks service route %q:\n%s", direct.Path, throughThePlug)
	}
}

// The compile-without-running tool is the same cascade with the SQL kept back,
// which is what makes it a probe for the compiler and not a second compiler.
func TestTheSQLToolCompilesWithoutRunning(t *testing.T) {
	svc := seededServiceWithModel(t)
	session := connect(t, svc)
	direct, err := svc.Query(t.Context(), service.QueryRequest{
		Question: "how many memories are there", SQLOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result := callTool(t, session, "roca_sql", map[string]any{
		"query": "how many memories are there",
	})
	text := renderedText(result)
	if direct.SQL == "" || !strings.Contains(text, direct.SQL) {
		t.Error("the sql tool returned no SQL")
	}
	if strings.Contains(text, "rows[") {
		t.Errorf("the sql tool ran what it was asked only to compile:\n%s", text)
	}
	assertNoStructuredEnvelope(t, result)
}

func TestQuestionGateIsSharedByBothMCPQuestionTools(t *testing.T) {
	session := connect(t, seededServiceWithModel(t))
	for _, tool := range []string{"roca_query", "roca_sql"} {
		got := callToolExpectingError(t, session, tool, map[string]any{"query": " \n "})
		if !strings.Contains(got, "question is empty") {
			t.Errorf("%s error = %q", tool, got)
		}
	}
}

func TestTheExecToolRunsTheSameValidatedSelectAsTheService(t *testing.T) {
	svc := seededService(t)
	session := connect(t, svc)
	ctx := context.Background()
	request := service.ExecRequest{
		SQL: "SELECT content FROM memories ORDER BY id LIMIT 1", MaxChars: 12,
	}

	direct, err := svc.Exec(ctx, request)
	if err != nil {
		t.Fatalf("Exec over the service: %v", err)
	}
	result := callTool(t, session, "roca_exec", map[string]any{
		"sql": request.SQL, "max_chars": request.MaxChars,
	})
	wantRows := axi.RowOutput(direct.Columns, direct.Rows)
	if text := renderedText(result); !strings.Contains(text, direct.SQL) || !strings.Contains(text, wantRows) {
		t.Errorf("plug answer differs from the validated service answer:\n%s\nwant rows:\n%s", text, wantRows)
	}
	assertNoStructuredEnvelope(t, result)
}

func TestEveryToolCallWritesACredentialFreeAuditRecord(t *testing.T) {
	svc := seededService(t)
	callTool(t, connect(t, svc), "roca_exec", map[string]any{
		"sql": "SELECT 'token=private-value' AS text",
	})
	raw := readSingleLog(t, svc.DataDir(), logfile.MCPAudit)
	text := string(raw)
	for _, want := range []string{`"tool":"roca_exec"`, `"ok":true`, `"row_count":1`, `"duration_ms":`} {
		if !strings.Contains(text, want) {
			t.Errorf("audit lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "private-value") {
		t.Fatalf("credential leaked into MCP audit: %s", text)
	}
}

func TestQueryAuditCarriesTheCurrentAttributionEnvelope(t *testing.T) {
	svc := seededServiceWithModel(t)
	callTool(t, connect(t, svc), "roca_query", map[string]any{"query": "how many memories"})
	raw := readSingleLog(t, svc.DataDir(), logfile.MCPAudit)
	text := string(raw)
	for _, want := range []string{`"path":"model"`, `"sql_provider":"fake"`, `"sql_model":"fake-model"`,
		`"sql_inference_ms":`, `"execution_ms":`} {
		if !strings.Contains(text, want) {
			t.Errorf("query audit lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, `"retried_sql":true`) || strings.Contains(text, `"retried":true`) {
		t.Fatalf("first-shot success was logged as a retry or rescue: %s", text)
	}
	for _, obsolete := range []string{`"engine":`, `"model":`, `"interpret_engine":`, `"interpret_model":`} {
		if strings.Contains(text, obsolete) {
			t.Errorf("query audit returned obsolete key %q: %s", obsolete, text)
		}
	}
}

func TestQueryAuditDistinguishesRetrySuccessFromRescue(t *testing.T) {
	for _, tc := range []struct {
		name        string
		answers     []string
		expectError bool
		want        []string
	}{
		{
			name: "retry success",
			answers: []string{
				"SELECT missing FROM memories LIMIT 1",
				"SELECT content FROM memories WHERE supersedes IS NULL LIMIT 1",
			},
			want: []string{`"path":"model"`, `"retried_sql":true`,
				`"first_model_sql":"SELECT missing FROM memories LIMIT 1"`,
				`"model_sql":"SELECT content FROM memories WHERE supersedes IS NULL LIMIT 1"`,
				`"retry_reason":"no such column:`, `missing`,
				`"sql_retry_inference_ms":`, `"sql_retry_provider_latency_ms":`},
		},
		{
			name: "rescue after retry",
			answers: []string{
				"SELECT missing FROM memories LIMIT 1",
				"SELECT still_missing FROM memories LIMIT 1",
			},
			expectError: true,
			want: []string{`"path":"keyword"`, `"retried":true`, `"retried_sql":true`,
				`"degraded":"invalid_sql"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := seededServiceWithScriptedModel(t, tc.answers)
			session := connect(t, svc)
			args := map[string]any{"query": "what decisions were made about the long dashes"}
			if tc.expectError {
				callToolExpectingError(t, session, "roca_query", args)
			} else {
				callTool(t, session, "roca_query", args)
			}
			text := string(readSingleLog(t, svc.DataDir(), logfile.MCPAudit))
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("query audit lacks %q: %s", want, text)
				}
			}
		})
	}
}

func readSingleLog(t *testing.T, dataDir, stream string) []byte {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dataDir, logfile.DirName, stream+"-*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("%s logs = %v, err=%v", stream, matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMalformedToolCallIsAuditedAsAFailure(t *testing.T) {
	svc := seededService(t)
	session := connect(t, svc)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "roca_query", Arguments: map[string]any{},
	})
	if err != nil || !result.IsError {
		t.Fatalf("malformed call result=%v err=%v", result, err)
	}
	matches, err := filepath.Glob(filepath.Join(svc.DataDir(), logfile.DirName, "mcp-audit-*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("MCP audit logs = %v, err=%v", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if text := string(raw); !strings.Contains(text, `"tool":"roca_query"`) || !strings.Contains(text, `"ok":false`) {
		t.Fatalf("failed call was not audited as a failure: %s", text)
	}
}

func TestUnavailableLLMIsAuditedAsDegradedNotOK(t *testing.T) {
	svc := seededServiceWithUnavailableModel(t)
	result := callToolResult(t, connect(t, svc), "roca_query", map[string]any{
		"query": "question no provider can answer",
	})
	if !result.IsError {
		t.Fatal("unavailable LLM did not fail the MCP tool result")
	}
	if !strings.Contains(renderedText(result), service.DegradedUnavailable) {
		t.Fatalf("fixture answered without the unavailable path: %s", renderedText(result))
	}
	assertNoStructuredEnvelope(t, result)
	matches, _ := filepath.Glob(filepath.Join(svc.DataDir(), logfile.DirName, "mcp-audit-*.jsonl"))
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"ok":false`) || !strings.Contains(text, `"degraded":"model_unavailable"`) {
		t.Fatalf("degraded call was audited optimistically: %s", text)
	}
}

func TestTheExecToolRefusesAWriteWithTheGatesVerdict(t *testing.T) {
	svc := seededService(t)
	statement := "DELETE FROM memories"

	_, directErr := svc.Exec(context.Background(), service.ExecRequest{SQL: statement})
	if directErr == nil {
		t.Fatal("the service accepted a write")
	}
	refused := callToolExpectingError(t, connect(t, svc), "roca_exec",
		map[string]any{"sql": statement})

	if refused != directErr.Error() {
		t.Errorf("the plug says %q and the gate says %q", refused, directErr)
	}
	if !strings.Contains(refused, "Only SELECT statements are allowed") {
		t.Errorf("the refusal %q is not the gate's existing verdict", refused)
	}
}

func TestADegradedQueryIsAnMCPToolErrorWithoutAnEnvelope(t *testing.T) {
	svc := openServiceWith(t, false, provider.Cascade{Providers: []provider.Provider{
		fakeModel{sql: "DELETE FROM memories"},
	}})
	if _, err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}

	result, err := connect(t, svc).CallTool(t.Context(), &mcp.CallToolParams{
		Name: "roca_query", Arguments: map[string]any{"query": "invalid sql sentinel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("degraded MCP result = isError %v", result.IsError)
	}
	if !strings.Contains(renderedText(result), service.DegradedInvalidSQL) {
		t.Fatalf("degraded answer omits %q: %s", service.DegradedInvalidSQL, renderedText(result))
	}
	assertNoStructuredEnvelope(t, result)
}

func TestMCPWarningsScrubTheWholeDataDirectoryPrefix(t *testing.T) {
	providers := provider.Cascade{
		Providers: []provider.Provider{fakeModel{sql: "SELECT 1 AS n LIMIT 1"}},
	}
	svc := openServiceWith(t, false, providers)
	providers.Warnings = []string{"unknown key in " + filepath.Join(svc.DataDir(), "config.toml")}
	svc.Close()
	svc = openServiceWith(t, false, providers)
	if _, err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}

	result := callTool(t, connect(t, svc), "roca_query", map[string]any{"query": "count one"})
	if output := renderedText(result); strings.Contains(output, svc.DataDir()) {
		t.Errorf("text output leaked data directory %q: %s", svc.DataDir(), output)
	}
	assertNoStructuredEnvelope(t, result)
}

func TestTheExecToolNeverCarriesTheDatabasePath(t *testing.T) {
	svc := seededService(t)
	dbPath := svc.DB().Path()
	session := connect(t, svc)

	result := callTool(t, session, "roca_exec", map[string]any{
		"sql": "SELECT content FROM memories LIMIT 1",
	})
	if text := renderedText(result); strings.Contains(text, dbPath) {
		t.Errorf("the readable output carries the database path %q: %s", dbPath, text)
	}

	pragmaRefusal := callToolExpectingError(t, session, "roca_exec",
		map[string]any{"sql": "SELECT file FROM pragma_database_list"})
	if strings.Contains(pragmaRefusal, dbPath) {
		t.Errorf("the pragma refusal carries the database path %q: %s", dbPath, pragmaRefusal)
	}

	refused := callToolExpectingError(t, session, "roca_exec",
		map[string]any{"sql": "SELECT * FROM " + dbPath})
	if strings.Contains(refused, dbPath) {
		t.Errorf("the tool error carries the database path %q: %s", dbPath, refused)
	}
}

func TestWritingThroughThePlugIsWritingThroughTheProduct(t *testing.T) {
	svc := seededService(t)
	session := connectAs(t, svc, "claude-code", "2.1.0")

	result := callTool(t, session, "roca_store", map[string]any{
		"layer": "discovery", "content": "written from a shell-less agent",
	})
	assertNoStructuredEnvelope(t, result)

	// The audit says it came from the plug, which is what tells this write from
	// the one the shell would have made.
	var storedID int64
	var agent, model, surface string
	if err := svc.DB().SQL().QueryRow(
		"SELECT id, source_agent, source_model, source_surface FROM memories WHERE content = ?", "written from a shell-less agent").
		Scan(&storedID, &agent, &model, &surface); err != nil {
		t.Fatalf("read the audit back: %v", err)
	}
	if storedID == 0 {
		t.Fatal("the write through the plug has no identity")
	}
	if agent != "claude-code" || model != service.UnknownAuthor || surface != service.SurfaceMCP {
		t.Errorf("authorship = %q/%q via %q, want claude-code/unknown via mcp", agent, model, surface)
	}
}

func TestHealthThroughThePlugIsTheSameDiagnosis(t *testing.T) {
	svc := seededService(t)
	session := connect(t, svc)

	result := callTool(t, session, "roca_health", nil)
	text := renderedText(result)
	if !strings.HasPrefix(text, "health: ") || !strings.Contains(text, "rows[") {
		t.Errorf("health tool returned no readable diagnosis:\n%s", text)
	}
	assertNoStructuredEnvelope(t, result)
}

// A missing argument is the caller's mistake, not the server's: it is answered
// as a tool error naming the argument, and the session survives it.
func TestAMissingArgumentIsAToolErrorAndTheSessionSurvives(t *testing.T) {
	session := connect(t, seededServiceWithModel(t))
	ctx := context.Background()

	failed, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "roca_query", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("a missing argument took the protocol down: %v", err)
	}
	if !failed.IsError {
		t.Fatal("a call with no arguments was not answered as a tool error")
	}
	if !strings.Contains(strings.ToLower(renderedText(failed)), "query") {
		t.Errorf("the error %q does not name the missing argument", renderedText(failed))
	}

	// And a correct call right after it works, which is what "the session is
	// still alive" means.
	after := queryThroughThePlug(t, session, map[string]any{
		"query": "how many memories are there",
	})
	if after == "" {
		t.Error("the session did not survive the mistaken call")
	}
}

// The read-only refusal is the service's and the plug only renders it: the two
// surfaces say the same thing because there is one refusal, not two.
func TestReadOnlyModeIsRefusedThroughThePlugToo(t *testing.T) {
	svc := readOnlyService(t)
	session := connect(t, svc)

	refused := callToolExpectingError(t, session, "roca_store", map[string]any{
		"layer": "discovery", "content": "this must not land",
	})
	fromTheService := storeThroughTheService(t, svc)

	if !strings.Contains(refused, "read-only") {
		t.Errorf("the plug's refusal %q does not name read-only mode", refused)
	}
	if !strings.Contains(refused, fromTheService) {
		t.Errorf("the plug says %q and the service says %q: two products",
			refused, fromTheService)
	}
}

// The handshake announces the product, never the library: an agent that reads
// back an SDK version has been told nothing about what answered it.
func TestTheHandshakeAnnouncesTheProductAndItsVersion(t *testing.T) {
	session := connect(t, seededService(t))

	server := session.InitializeResult().ServerInfo
	if server.Name != mcpplug.ServerName {
		t.Errorf("name = %q, want %q", server.Name, mcpplug.ServerName)
	}
	if server.Version != "0.0.0-test" {
		t.Errorf("version = %q, want the product's, not a library's", server.Version)
	}
	if version := session.InitializeResult().ProtocolVersion; version == "" {
		t.Error("the handshake declares no protocol version")
	}
}

// The plug keeps nothing between calls. Two
// sessions of the same server see exactly the same thing, in either order.
func TestTheServerKeepsNoStateBetweenSessions(t *testing.T) {
	svc := seededService(t)
	first := queryThroughThePlug(t, connect(t, svc), map[string]any{
		"query": "how many memories are there",
	})
	second := queryThroughThePlug(t, connect(t, svc), map[string]any{
		"query": "how many memories are there",
	})

	if first == "" || second == "" || answerBody(first) != answerBody(second) {
		t.Errorf("two sessions gave different answers: %q / %q", first, second)
	}
}

// --- the harness ---

func connect(t *testing.T, svc *service.Service) *mcp.ClientSession {
	return connectAs(t, svc, "test", "0")
}

func connectAs(t *testing.T, svc *service.Service, name, version string) *mcp.ClientSession {
	t.Helper()
	server := mcpplug.New(svc, mcpplug.Build{
		Version: "0.0.0-test", Commit: "0123456789abcdef",
	})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect the server: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: name, Version: version}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect the client: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func listTools(t *testing.T, session *mcp.ClientSession) *mcp.ListToolsResult {
	t.Helper()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	return tools
}

func callTool(t *testing.T, session *mcp.ClientSession, name string,
	arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result := callToolResult(t, session, name, arguments)
	if result.IsError {
		t.Fatalf("call %s answered with an error: %s", name, renderedText(result))
	}
	return result
}

func callToolExpectingError(t *testing.T, session *mcp.ClientSession, name string,
	arguments map[string]any) string {
	t.Helper()
	result := callToolResult(t, session, name, arguments)
	if !result.IsError {
		t.Fatalf("call %s was expected to be refused and was not", name)
	}
	return renderedText(result)
}

func callToolResult(t *testing.T, session *mcp.ClientSession, name string,
	arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(),
		&mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result
}

func queryThroughThePlug(t *testing.T, session *mcp.ClientSession,
	arguments map[string]any) string {
	t.Helper()
	result := callTool(t, session, "roca_query", arguments)
	assertNoStructuredEnvelope(t, result)
	return renderedText(result)
}

func answerBody(text string) string {
	var body []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "route ") {
			body = append(body, line)
		}
	}
	return strings.Join(body, "\n")
}

func asJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %v: %v", value, err)
	}
	return string(encoded)
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func renderedText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func storeThroughTheService(t *testing.T, svc *service.Service) string {
	t.Helper()
	_, err := svc.Store(context.Background(), service.StoreRequest{
		Layer: "discovery", Content: "this must not land",
		Authorship: service.Authorship{Surface: service.SurfaceCLI},
	})
	if err == nil {
		t.Fatal("the service accepted a write in read-only mode")
	}
	return err.Error()
}

func seededService(t *testing.T) *service.Service {
	t.Helper()
	svc := openService(t, false)
	if _, err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, seed := range []struct{ layer, content string }{
		{"project", "the team hates long dashes in the generated text"},
		{"discovery", "adoption compares structure, never the text of the DDL"},
	} {
		if _, err := svc.Store(context.Background(), service.StoreRequest{
			Layer: seed.layer, Content: seed.content,
			Authorship: service.Authorship{Surface: service.SurfaceCLI},
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return svc
}

func readOnlyService(t *testing.T) *service.Service {
	t.Helper()
	if _, err := openService(t, false).Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return openService(t, true)
}

// openService opens the installation of this test. The directory is fixed per
// test so that the read-only reopening lands on the database the first one
// created.
func openService(t *testing.T, readOnly bool) *service.Service {
	return openServiceWith(t, readOnly, provider.Cascade{})
}

// openServiceWith opens the installation with an optional model cascade, for
// the tools that need a provider to generate SQL.
func openServiceWith(t *testing.T, readOnly bool, providers provider.Cascade) *service.Service {
	t.Helper()
	dir := theDirectoryOf(t)
	options := service.Options{
		DBPath:    filepath.Join(dir, "roca.db"),
		BackupDir: filepath.Join(dir, "backups"),
		DataDir:   dir,
		Version:   "0.0.0-test",
		Commit:    "0123456789abcdef",
		ReadOnly:  readOnly,
		Providers: providers,
	}
	svc, err := service.Open(options)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

// fakeModel answers every chat with one canned SELECT, so the model-path tools
// have a provider to ask in the hermetic fixture.
type fakeModel struct{ sql string }

func (f fakeModel) Name() string                             { return "fake" }
func (f fakeModel) ModelID() string                          { return "fake-model" }
func (f fakeModel) Ready(context.Context) provider.Readiness { return provider.Readiness{Ready: true} }
func (f fakeModel) Models(context.Context) provider.ModelReport {
	return provider.ModelReport{Ready: true, Models: []string{"fake-model"}}
}
func (f fakeModel) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{Content: f.sql, Provider: "fake", ModelID: "fake-model"}, nil
}

type scriptedModel struct {
	answers []string
	calls   int
}

func (m *scriptedModel) Name() string    { return "fake" }
func (m *scriptedModel) ModelID() string { return "fake-model" }
func (m *scriptedModel) Ready(context.Context) provider.Readiness {
	return provider.Readiness{Ready: true}
}
func (m *scriptedModel) Models(context.Context) provider.ModelReport {
	return provider.ModelReport{Ready: true, Models: []string{"fake-model"}}
}
func (m *scriptedModel) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	answer := m.answers[min(m.calls, len(m.answers)-1)]
	m.calls++
	return provider.ChatResponse{Content: answer, Provider: m.Name(), ModelID: m.ModelID()}, nil
}

type unavailableModel struct{}

func (unavailableModel) Name() string    { return "unavailable" }
func (unavailableModel) ModelID() string { return "offline-model" }
func (unavailableModel) Ready(context.Context) provider.Readiness {
	return provider.Readiness{Reason: "offline", Action: "start it"}
}
func (unavailableModel) Models(context.Context) provider.ModelReport {
	return provider.ModelReport{Reason: "offline"}
}
func (unavailableModel) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, fmt.Errorf("must not call an unavailable provider")
}

func seededServiceWithUnavailableModel(t *testing.T) *service.Service {
	t.Helper()
	svc := openServiceWith(t, false, provider.Cascade{Providers: []provider.Provider{unavailableModel{}}})
	if _, err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return svc
}

// seededServiceWithModel is the seeded installation with a model that answers a
// counting SELECT, for the tools whose contract is producing SQL.
func seededServiceWithModel(t *testing.T) *service.Service {
	t.Helper()
	svc := openServiceWith(t, false, provider.Cascade{Providers: []provider.Provider{
		fakeModel{sql: "SELECT COUNT(*) AS n FROM memories WHERE supersedes IS NULL LIMIT 1"},
	}})
	if _, err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return svc
}

func seededServiceWithScriptedModel(t *testing.T, answers []string) *service.Service {
	t.Helper()
	model := &scriptedModel{answers: answers}
	svc := openServiceWith(t, false, provider.Cascade{Providers: []provider.Provider{model}})
	if _, err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := svc.Store(context.Background(), service.StoreRequest{
		Layer: "project", Content: "the team hates long dashes in generated text",
		Authorship: service.Authorship{Surface: service.SurfaceMCP},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return svc
}

var directories = map[string]string{}

func theDirectoryOf(t *testing.T) string {
	t.Helper()
	if dir, ok := directories[t.Name()]; ok {
		return dir
	}
	dir := t.TempDir()
	directories[t.Name()] = dir
	t.Cleanup(func() { delete(directories, t.Name()) })
	return dir
}
