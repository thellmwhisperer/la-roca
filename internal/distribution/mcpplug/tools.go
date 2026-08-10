package mcpplug

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// The five tools of v1 each have a concrete caller. Adding one changes the
// product surface; `roca_list_runs` is out because
// `runs` is v2 and this binary creates no such table.
var (
	execTool = &mcp.Tool{
		Name: "roca_exec",
		Description: "Run SQL returned by roca_sql under the same read-only gate as " +
			"roca exec. Every statement is validated before it reaches the database.",
	}
	queryTool = &mcp.Tool{
		Name: "roca_query",
		Description: "Query La Roca, the local memory of past conversations, " +
			"decisions and learnings across every project on this machine.\n\n" +
			"Good questions are short and specific:\n" +
			"- \"count memories by project\" — analytics\n" +
			"- \"what do we know about ffmpeg\" — concept search\n" +
			"- \"what feedback do we have\" — ask in any language; the corpus answers with what it holds\n\n" +
			"Keep it under 15 words and one concept per question. " +
			"With zero results, rephrase with different keywords.",
	}
	sqlTool = &mcp.Tool{
		Name: "roca_sql",
		Description: "Turn a natural-language question into the SQL the model " +
			"would run, without running it. For agents that would rather run it " +
			"themselves (`roca exec` runs it under the same read-only gate).",
	}
	storeTool = &mcp.Tool{
		Name: "roca_store",
		Description: "Store one memory: a discovery, a pattern, a decision or a handoff. " +
			"Identical content already stored in the same layer, status and project is " +
			"not written twice.",
	}
	healthTool = &mcp.Tool{
		Name: "roca_health",
		Description: "Run the non-destructive checks over live data and return the " +
			"structured diagnosis. It is what an agent that cannot run `roca doctor` " +
			"asks instead.",
	}
)

// execArgs is the SQL and text budget accepted by the command-line exec path.
// No MCP-only option belongs here: both surfaces build the same request.
type execArgs struct {
	SQL      string `json:"sql" jsonschema:"the SELECT statement to validate and run"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"character budget per text field"`
}

func (a execArgs) request() service.ExecRequest {
	return service.ExecRequest{SQL: a.SQL, MaxChars: a.MaxChars}
}

// queryArgs is what an agent sends to ask a question. The defaults live in the
// schema and not in a handler: the SDK applies them before the call, so the
// plug stays a passthrough and the declared surface is the honest one.
type queryArgs struct {
	Query    string `json:"query" jsonschema:"short natural-language question, under 15 words"`
	Layer    string `json:"layer,omitempty" jsonschema:"restrict the answer to one memory layer"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"character budget per text field,default=500"`
}

func (a queryArgs) request() service.QueryRequest {
	return service.QueryRequest{
		Question: a.Query,
		Layer:    a.Layer,
		MaxChars: a.MaxChars,
	}
}

// sqlArgs is the same question with the running kept back. SQLOnly is fixed
// here and not asked for: a tool whose whole purpose is the SQL without running
// does not take "run it" as a parameter.
type sqlArgs struct {
	Query string `json:"query" jsonschema:"short natural-language question, under 15 words"`
	Layer string `json:"layer,omitempty" jsonschema:"restrict the answer to one memory layer"`
}

func (a sqlArgs) request() service.QueryRequest {
	return service.QueryRequest{
		Question: a.Query,
		Layer:    a.Layer,
		SQLOnly:  true,
	}
}

// storeArgs is one memory to write. Surface is fixed to the plug: it is the
// audit and it is not the caller's to declare.
type storeArgs struct {
	Layer   string `json:"layer" jsonschema:"user, feedback, project, pattern, pill, discovery, handoff, question, review or issue"`
	Content string `json:"content" jsonschema:"the content of the memory, in natural language"`
	Origin  string `json:"origin,omitempty" jsonschema:"who creates it: human, agent or cron,default=agent"`

	SourceAgent string         `json:"source_agent,omitempty" jsonschema:"which agent is writing it"`
	Project     string         `json:"project,omitempty" jsonschema:"project scope; omit for global"`
	Status      string         `json:"status,omitempty" jsonschema:"active, pending or resolved,default=active"`
	Supersedes  int64          `json:"supersedes,omitempty" jsonschema:"id of the memory this one replaces"`
	Metadata    map[string]any `json:"metadata,omitempty" jsonschema:"structured tags"`
}

func (a storeArgs) request() service.StoreRequest {
	return service.StoreRequest{
		Layer:       a.Layer,
		Content:     a.Content,
		Origin:      a.Origin,
		SourceAgent: a.SourceAgent,
		Project:     a.Project,
		Status:      a.Status,
		Supersedes:  a.Supersedes,
		Metadata:    a.Metadata,
		Surface:     service.SurfaceMCP,
	}
}

// healthArgs is the diagnosis' only knob.
type healthArgs struct {
	MaxRows int `json:"max_rows,omitempty" jsonschema:"sample rows per check,default=10"`
}

func (a healthArgs) request() service.HealthRequest {
	return service.HealthRequest{MaxRows: a.MaxRows}
}
