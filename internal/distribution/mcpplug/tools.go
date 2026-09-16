package mcpplug

import (
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/jsonid"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// The tools of v1 each have a concrete caller. Adding one changes the
// product surface; `roca_list_runs` is out because
// `runs` is v2 and this binary creates no such table.
var (
	execTool = &mcp.Tool{
		Name: "roca_exec",
		Description: "Run SQL returned by roca_sql under the same read-only gate as " +
			"roca exec. Every statement is validated before it reaches the database.",
	}
	exploreTool = &mcp.Tool{
		Name: "roca_explore",
		Description: "Investigate one concept with the same full output as roca explore: " +
			"grounded prose, deterministic terrain, next trail hints, and the generated SQL. " +
			"Set deep for the full terrain map and 2-3 next probes.",
	}
	queryTool = &mcp.Tool{
		Name: "roca_query",
		Description: "Hybrid full-text plus vector search with labeled evidence. Use this " +
			"only when exact terms matter; start with roca_vector_query for the fast semantic " +
			"leg. This tool adds the full-text leg and fusion. Good questions are short and " +
			"specific. The hard input cap is 1000 characters. Each hit names which legs found it.",
	}
	handoffLatestTool = &mcp.Tool{
		Name: "roca_handoff_latest",
		Description: "Load the current unsuperseded handoff for one project. Omit project " +
			"to use the working-directory basename. An unknown project names the projects " +
			"that do have handoffs and returns nothing else.",
	}
	pillShowTool = &mcp.Tool{
		Name: "roca_pill_show",
		Description: "Load one complete pill by slug for a project. Omit project to use " +
			"the working-directory basename. An unknown slug lists the known slugs.",
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
			"compact AXI diagnosis. It is what an agent that cannot run `roca doctor` " +
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

// queryArgs is what an agent sends to ask a question. A zero budget reaches the
// service, where the shared default is applied for every surface.
type queryArgs struct {
	Query       string `json:"query,omitempty" jsonschema:"non-empty natural-language question, preferably under 15 words, maximum 1000 characters"`
	Question    string `json:"question,omitempty" jsonschema:"alias of query"`
	Text        string `json:"text,omitempty" jsonschema:"alias of query"`
	Top         int    `json:"top,omitempty" jsonschema:"number of fused hits to return, default 10"`
	K           int    `json:"k,omitempty" jsonschema:"alias of top"`
	Limit       int    `json:"limit,omitempty" jsonschema:"alias of top"`
	RequireBoth bool   `json:"require_both,omitempty" jsonschema:"keep only hits found by both FTS and vector"`
	MaxChars    int    `json:"max_chars,omitempty" jsonschema:"character budget per snippet"`
	Databases   string `json:"databases,omitempty" jsonschema:"comma list of attached database names (corpus,ops), or all"`
}

// exploreArgs declares the investigation mission. The tool name declares
// exploration; Deep distinguishes its light and full-care variants.
type exploreArgs struct {
	Query     string `json:"query" jsonschema:"one non-empty concept, preferably a single bare word, maximum 1000 characters"`
	Layer     string `json:"layer,omitempty" jsonschema:"restrict the investigation to one memory layer"`
	MaxChars  int    `json:"max_chars,omitempty" jsonschema:"character budget per text field"`
	Deep      bool   `json:"deep,omitempty" jsonschema:"use the full terrain map and propose 2-3 next probes"`
	Databases string `json:"databases,omitempty" jsonschema:"comma list of attached database names (corpus,ops), or all"`
}

func (a queryArgs) request() service.SearchRequest {
	return service.SearchRequest{
		Question:    firstNonEmpty(a.Query, a.Question, a.Text),
		Top:         firstPositive(a.Top, a.K, a.Limit),
		RequireBoth: a.RequireBoth,
		MaxChars:    a.MaxChars,
		Databases:   mustParseDatabases(a.Databases),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

type handoffLatestArgs struct {
	Project string `json:"project,omitempty" jsonschema:"project scope; omit to use the working-directory basename"`
}

type pillShowArgs struct {
	Slug    string `json:"slug" jsonschema:"pill slug to load"`
	Project string `json:"project,omitempty" jsonschema:"project scope; omit to use the working-directory basename"`
}

func mustParseDatabases(raw string) []string {
	names, err := service.ParseDatabaseList(raw)
	if err != nil {
		return []string{raw}
	}
	return names
}

// sqlArgs is the same question with the running kept back. SQLOnly is fixed
// here and not asked for: a tool whose whole purpose is the SQL without running
// does not take "run it" as a parameter.
type sqlArgs struct {
	Query     string `json:"query" jsonschema:"non-empty natural-language question, preferably under 15 words, maximum 1000 characters"`
	Layer     string `json:"layer,omitempty" jsonschema:"restrict the answer to one memory layer"`
	Databases string `json:"databases,omitempty" jsonschema:"comma list of attached database names (corpus,ops), or all"`
}

func (a sqlArgs) request() service.QueryRequest {
	return service.QueryRequest{
		Question:  a.Query,
		Layer:     a.Layer,
		SQLOnly:   true,
		Databases: mustParseDatabases(a.Databases),
	}
}

// storeArgs is one memory to write. The identity card comes from the connected
// session rather than these caller-controlled arguments.
type storeArgs struct {
	Layer   string `json:"layer" jsonschema:"user, feedback, project, pattern, pill, discovery, handoff, question, review or issue"`
	Content string `json:"content" jsonschema:"the content of the memory, in natural language"`
	Origin  string `json:"origin,omitempty" jsonschema:"who creates it: human, agent, cron or plugin:<name>,default=agent"`

	Project    string         `json:"project,omitempty" jsonschema:"project scope; omit for global"`
	Status     string         `json:"status,omitempty" jsonschema:"active, pending or resolved,default=active"`
	Supersedes jsonid.Decimal `json:"supersedes,omitempty" jsonschema:"id of the memory this one replaces, as a decimal string; a JSON number is still accepted"`
	Metadata   map[string]any `json:"metadata,omitempty" jsonschema:"structured tags; agent, model and surface belong to the identity card and are refused here"`
}

func (a storeArgs) request(authorship service.Authorship) service.StoreRequest {
	return service.StoreRequest{
		Layer:      a.Layer,
		Content:    a.Content,
		Origin:     a.Origin,
		Authorship: authorship,
		Project:    a.Project,
		Status:     a.Status,
		Supersedes: a.Supersedes.Int64(),
		Metadata:   a.Metadata,
	}
}

func authorshipFromRequest(req *mcp.CallToolRequest) service.Authorship {
	authorship := service.Authorship{
		Agent: service.UnknownAuthor, Model: service.UnknownAuthor, Surface: service.SurfaceMCP,
	}
	if req == nil || req.Session == nil || req.Session.InitializeParams() == nil ||
		req.Session.InitializeParams().ClientInfo == nil {
		return authorship
	}
	if name := strings.TrimSpace(req.Session.InitializeParams().ClientInfo.Name); name != "" {
		authorship.Agent = name
	}
	return authorship
}

// healthArgs is the diagnosis' only knob.
type healthArgs struct {
	MaxRows int `json:"max_rows,omitempty" jsonschema:"sample rows per check,default=10"`
}

func (a healthArgs) request() service.HealthRequest {
	return service.HealthRequest{MaxRows: a.MaxRows}
}

func init() {
	schema, err := jsonschema.For[storeArgs](nil)
	if err != nil {
		panic("roca_store input schema: " + err.Error())
	}
	if prop := schema.Properties["supersedes"]; prop != nil {
		prop.Type = ""
		prop.Types = []string{"string", "integer"}
	}
	storeTool.InputSchema = schema
}
