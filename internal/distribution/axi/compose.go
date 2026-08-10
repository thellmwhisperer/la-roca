/*
@overview Composes query, SQL, health, and store results into AXI text. ~180 lines, 5 public symbols.

	READING GUIDE
	-------------
	1. Start at Query for the natural-language answer contract
	2. Read Exec for direct SQL rendering
	3. Read Health and Store for the remaining result composers

	MAIN FLOW
	---------
	service result -> provenance/message -> optional prose -> TOON rows -> contextual help

	PUBLIC API
	----------
	QueryPreamble  Render query provenance and route context
	Query          Render a query, optional interpretation, evidence, and help
	Exec           Render a gated SELECT and its rows
	Health         Render live health checks
	Store          Render a stored-memory outcome

	INTERNALS
	---------
	appendLine, queryTail

@exports QueryPreamble, Query, Exec, Health, Store
@deps standard formatting/sorting/strings, internal human/service
*/
package axi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/human"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// -- 1/3 CORE · Query and QueryPreamble -- <- START HERE

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
	if res.Engine != "" {
		appendLine(&b, fmt.Sprintf("route %s · provider %s · model %s · %s",
			res.Path, res.Engine, res.Model, human.Duration(res.LatencyMS)))
	} else {
		appendLine(&b, fmt.Sprintf("route %s · %s", res.Path, human.Duration(res.LatencyMS)))
	}
	if res.Degraded != "" {
		appendLine(&b, "degraded: "+res.Degraded)
	}
	if res.Message != "" && res.RowCount > 0 {
		appendLine(&b, res.Message)
	}
	return b.String()
}

// queryTail is the rows table and the help that follow the preamble when a
// query answered with rows. Both the shell and MCP surface reach it through
// Query; full shell output adds prose above it without hiding the evidence.
func queryTail(res service.QueryResult) string {
	var b strings.Builder
	appendLine(&b, RowOutput(res.Columns, res.Rows, res.Question))
	if !(res.RowCount == 1 && len(res.Columns) == 1) {
		appendLine(&b, QueryHelp(res))
	}
	return b.String()
}

// Query renders the AXI text for a query result: the route preamble and then
// whichever tail the answer takes. A question the model never lifted is only
// its message; a compile-only answer is its SQL; an empty match is the message
// and the help to broaden it; and a matched answer is its rows and help. When
// the caller passes prose, that interpretation is added above the evidence.
//
// The shell passes the second inference call's prose; the plug passes the empty
// string, because an agent reads rows and writes its own.
func Query(res service.QueryResult, prose string) string {
	if res.Path == service.PathUnresolved {
		return res.Message
	}
	var b strings.Builder
	appendLine(&b, QueryPreamble(res))
	switch {
	case res.Match == "":
		appendLine(&b, res.SQL)
	case res.RowCount == 0:
		appendLine(&b, res.Message)
		appendLine(&b, QueryHelp(res))
	case prose != "":
		appendLine(&b, prose)
		appendLine(&b, queryTail(res))
	default:
		appendLine(&b, queryTail(res))
	}
	return b.String()
}

// -/ 1/3

// -- 2/3 HELPER · Exec --

// Exec renders the AXI text for a SELECT run under the read-only gate: the SQL
// that ran, its rows, the count and latency, and the help to reach the envelope
// or expand the fields.
func Exec(res service.ExecResult) string {
	var b strings.Builder
	appendLine(&b, res.SQL)
	appendLine(&b, RowOutput(res.Columns, res.Rows))
	appendLine(&b, fmt.Sprintf("%d rows · %s", res.RowCount, human.Duration(res.LatencyMS)))
	if res.RowCount > 0 && !(res.RowCount == 1 && len(res.Columns) == 1) {
		appendLine(&b, RenderHelp(
			"Run `roca exec \"<SELECT>\" --json` for the complete result envelope",
			"Run `roca exec \"<SELECT>\" --max-chars 2000` to expand text fields"))
	}
	return b.String()
}

// -/ 2/3

// -- 3/3 HELPER · Health and Store --

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

// -/ 3/3
