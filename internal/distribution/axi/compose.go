package axi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// appendLine writes line to b on its own line, skipping empties. It never
// leaves a leading or trailing blank line, so a composer's output is exactly
// its meaningful lines joined by newlines.
func appendLine(b *strings.Builder, line string) {
	if line == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(line)
}

// QueryPreamble is the route narration that sits above a query's rows: the
// configuration warnings, the note about which providers were tried, the route
// line that names the path and the model, the degradation verdict and the
// message that leads the answer when there is one. It is the half of the query
// text that is not the rows, kept here so the shell and the plug print the same
// provenance.
func QueryPreamble(res service.QueryResult) string {
	var b strings.Builder
	for _, warning := range res.Warnings {
		appendLine(&b, "warning: "+warning)
	}
	appendLine(&b, res.ProviderNote)
	if len(res.Databases) > 0 {
		appendLine(&b, "databases: "+strings.Join(res.Databases, ", "))
	}
	if len(res.OmittedDatabases) > 0 {
		appendLine(&b, "databases omitted: "+strings.Join(res.OmittedDatabases, ", "))
	}
	appendLine(&b, "route "+res.Path)
	if res.Engine != "" {
		appendLine(&b, fmt.Sprintf("SQL · provider %s · model %s · %s",
			res.Engine, res.Model, Duration(res.SQLInferenceMS)))
	}
	if res.RetriedSQL {
		reason := "gate rejection"
		if res.RetryType == service.RetryExecutionError {
			reason = "execution error"
		}
		appendLine(&b, "SQL retry after "+reason+" · "+Duration(res.SQLRetryInferenceMS))
	}
	if res.Match != "" {
		appendLine(&b, "search · "+Duration(res.ExecutionMS))
	}
	// Who read the result rows, when that is somebody else. An installation
	// splits the two inferences so the rows stay on one machine, and a claim
	// like that is worth nothing unless the answer names who received them.
	appendLine(&b, res.InterpretNote)
	if res.InterpretEngine != "" {
		appendLine(&b, fmt.Sprintf("answer · provider %s · model %s · %s",
			res.InterpretEngine, res.InterpretModel, Duration(res.InterpretationMS)))
	}
	if res.Degraded != "" {
		appendLine(&b, "degraded: "+res.Degraded)
	}
	if len(res.Repaired) > 0 {
		appendLine(&b, "repaired: "+strings.Join(res.Repaired, ", "))
	}
	if res.Message != "" && res.RowCount > 0 {
		appendLine(&b, res.Message)
	}
	return b.String()
}

// queryTail is the rows table and the help that follow the preamble when a
// query answered with rows. Both the default shell mode and MCP reach it.
func queryTail(res service.QueryResult, help func(service.QueryResult) string) string {
	var b strings.Builder
	appendLine(&b, RowOutput(res.Columns, res.Rows, res.Question))
	if !(res.RowCount == 1 && len(res.Columns) == 1) {
		appendLine(&b, help(res))
	}
	return b.String()
}

// Query renders the AXI text for a query result: the route preamble and then
// whichever tail the answer takes. A question the model never lifted is only
// its message; a compile-only answer is its SQL; an empty match is the message
// and the help to broaden it; and a matched answer is its rows and help. When
// the caller passes prose, that interpretation replaces the data tail.
//
// The shell passes the second inference call's prose; the plug passes the empty
// string, because an agent reads rows and writes its own.
func Query(res service.QueryResult, prose string) string {
	return composeQuery(res, prose, QueryHelp)
}

// MCPQuery renders the same answer with next steps a shell-less agent can use.
func MCPQuery(res service.QueryResult) string {
	return composeQuery(res, "", MCPQueryHelp)
}

// Explore is the one text contract shared byte-for-byte by CLI and MCP. It
// always declares the selected mode and the generated SQL before the grounded
// investigation prose. Rows appear only as the failure floor when the second
// inference could not answer.
func Explore(res service.QueryResult) string {
	var b strings.Builder
	appendLine(&b, "mode: "+res.Mode)
	if res.Path == service.PathUnresolved || res.Path == service.PathAsk {
		appendLine(&b, res.Message)
		return b.String()
	}
	appendLine(&b, QueryPreamble(res))
	if res.Path == service.PathRefused {
		appendLine(&b, res.Message)
		return b.String()
	}
	generated := res.SQL
	if generated == "" {
		generated = res.CleanedSQL
	}
	if generated == "" {
		generated = res.ModelSQL
	}
	if generated != "" {
		appendLine(&b, "generated SQL:\n"+generated)
	}
	if res.Interpretation != "" {
		appendLine(&b, res.Interpretation)
		return b.String()
	}
	if res.RowCount == 0 {
		appendLine(&b, res.Message)
		return b.String()
	}
	appendLine(&b, RowOutput(res.Columns, res.Rows, res.Question))
	return b.String()
}

func composeQuery(res service.QueryResult, prose string, help func(service.QueryResult) string) string {
	if res.Path == service.PathUnresolved || res.Path == service.PathAsk {
		return res.Message
	}
	var b strings.Builder
	appendLine(&b, QueryPreamble(res))
	if res.Path == service.PathRefused {
		appendLine(&b, res.Message)
		return b.String()
	}
	switch {
	case res.Match == "":
		appendLine(&b, res.SQL)
	case res.RowCount == 0:
		appendLine(&b, res.Message)
		appendLine(&b, help(res))
	case prose != "":
		appendLine(&b, prose)
	default:
		appendLine(&b, queryTail(res, help))
	}
	return b.String()
}

// Exec renders the AXI text for a SELECT run under the read-only gate: the SQL
// that ran, its rows, the count and latency, and the help to reach the envelope
// or expand the fields.
func Exec(res service.ExecResult) string {
	return exec(res, []string{
		"Run `roca exec \"<SELECT>\" --json` for the complete result envelope",
		"Run `roca exec \"<SELECT>\" --max-chars 2000` to expand text fields",
	}, false)
}

// ExecWithHelp renders the standard SELECT envelope with caller-specific next
// commands. Remote exec and cross return the same envelope, but their useful
// follow-ups must keep the remote name and scatter scope instead of pointing at
// an unrelated local command.
func ExecWithHelp(res service.ExecResult, help ...string) string {
	return exec(res, help, true)
}

// MCPExec renders an executed SELECT with MCP-native next steps.
func MCPExec(res service.ExecResult) string {
	return exec(res, []string{
		"Call roca_exec again with max_chars 2000 to expand text fields",
	}, false)
}

// Search renders the zero-inference hybrid query envelope: which engines ran,
// the rarity-selected terms, labeled hits, and the next deterministic commands.
func Search(res service.SearchResult) string {
	return searchText(res, []string{
		"Run `roca query " + shellArg(res.Question) + " --json` for the complete result envelope",
		"Run `roca query " + shellArg(res.Question) + " --require-both` to keep only dual-confirmed hits",
	})
}

// MCPSearch is the same envelope with MCP-native next steps.
func MCPSearch(res service.SearchResult) string {
	return searchText(res, []string{
		"Call roca_query again with require_both to keep only dual-confirmed hits",
		"Call roca_exec with a SELECT to frame a cited source",
	})
}

func searchText(res service.SearchResult, help []string) string {
	var b strings.Builder
	engines := strings.Join(res.Engines, ",")
	if engines == "" {
		engines = "none"
	}
	appendLine(&b, fmt.Sprintf("search %s · engines %s · %s", hybridMode(res.Engines), engines, Duration(res.LatencyMS)))
	if len(res.Databases) > 0 {
		appendLine(&b, "databases: "+strings.Join(res.Databases, ", "))
	}
	for _, notice := range res.Notices {
		appendLine(&b, "notice: "+notice)
	}
	if len(res.Terms) > 0 {
		appendLine(&b, "terms["+fmt.Sprintf("%d", len(res.Terms))+"]: "+strings.Join(res.Terms, ", "))
	}
	rows := make([]map[string]any, 0, len(res.Hits))
	for _, hit := range res.Hits {
		row := map[string]any{
			"rank": hit.Rank, "source": hit.Source, "legs": strings.Join(hit.Legs, "+"),
			"snippet": hit.Snippet,
		}
		if hit.Consensus {
			row["consensus"] = true
		}
		if hit.VectorScore != nil {
			row["vector_score"] = *hit.VectorScore
		}
		if hit.VectorRank != nil {
			row["vector_rank"] = *hit.VectorRank
		}
		if hit.FTSRank != nil {
			row["fts_rank"] = *hit.FTSRank
		}
		rows = append(rows, row)
	}
	appendLine(&b, RowOutput([]string{
		"rank", "source", "legs", "consensus", "vector_score", "vector_rank", "fts_rank", "snippet",
	}, rows, res.Terms...))
	if len(res.Hits) == 0 {
		appendLine(&b, "no matches in memory for that search")
	}
	if len(help) > 0 && (len(res.Hits) != 1) {
		appendLine(&b, RenderHelp(help...))
	}
	return b.String()
}

func hybridMode(engines []string) string {
	hasFTS, hasVector := false, false
	for _, engine := range engines {
		if engine == "fts" {
			hasFTS = true
		}
		if engine == "vector" {
			hasVector = true
		}
	}
	switch {
	case hasFTS && hasVector:
		return "hybrid"
	case hasVector:
		return "vector"
	default:
		return "fts"
	}
}

func exec(res service.ExecResult, help []string, alwaysHelp bool) string {
	var b strings.Builder
	appendLine(&b, res.SQL)
	if len(res.Databases) > 0 {
		appendLine(&b, "databases: "+strings.Join(res.Databases, ", "))
	}
	appendLine(&b, RowOutput(res.Columns, res.Rows))
	appendLine(&b, fmt.Sprintf("%s · %s",
		Quantity(int64(res.RowCount), "row"), Duration(res.LatencyMS)))
	if len(help) > 0 && (alwaysHelp || res.RowCount > 0 && !(res.RowCount == 1 && len(res.Columns) == 1)) {
		appendLine(&b, RenderHelp(help...))
	}
	return b.String()
}

// Health renders the AXI text for a diagnosis: the overall status and the
// check table a human scans. The count is the truth; the summary is what makes
// each row actionable.
func Health(res service.HealthReport) string {
	var b strings.Builder
	appendLine(&b, "health: "+res.Status)
	names := make([]string, 0, len(res.Checks))
	for name := range res.Checks {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		check := res.Checks[name]
		rows = append(rows, map[string]any{
			"status": check.Status, "check": name,
			"count": check.Count, "summary": check.Summary,
		})
	}
	appendLine(&b, RowOutput([]string{"status", "check", "count", "summary"}, rows))
	return b.String()
}

// Store renders the AXI text for a stored memory: its identity and its layer,
// and whether the write created it or found it already there. A row table over
// one identity is ceremony, so it is a single line.
func Store(res service.StoreResult) string {
	if res.Skipped {
		return fmt.Sprintf("already stored: memory %d in layer %s", res.ID, res.Layer)
	}
	return fmt.Sprintf("stored: memory %d in layer %s", res.ID, res.Layer)
}
