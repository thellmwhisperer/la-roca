package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
)

func (env *environment) vectorizationStatus(ctx context.Context) (vector.Vectorization, error) {
	state, err := env.resolveStateDir()
	if err != nil {
		return vector.Vectorization{}, err
	}
	pluginRoot, err := env.resolvePluginRoot()
	if err != nil {
		return vector.Vectorization{}, err
	}
	return vector.ReportVectorization(ctx, vector.StatusRequest{
		PluginRoot: pluginRoot,
		StateDir:   state,
	})
}

func statusHelp(report vector.Vectorization) []string {
	lines := []string{"Run `roca vector status --json` for the complete result envelope"}
	needsInstall := false
	var compact []string
	var stale []string
	var live []string
	for _, row := range report.Databases {
		switch row.State {
		case vector.StateEmpty, vector.StateOutdated, vector.StateBuilding:
			needsInstall = true
		}
		name := row.Plugin
		if row.Database != "" {
			name += "/" + row.Database
		}
		if row.CompactRecommended {
			compact = append(compact, name)
		}
		switch row.IndexLock {
		case vector.IndexLockStale:
			stale = append(stale, name)
		case vector.IndexLockLive:
			live = append(live, name)
		}
	}
	if !report.Worker.Running && needsInstall {
		lines = append(lines, "Run `roca vector install` to start or resume embedding")
	}
	if len(compact) > 0 {
		lines = append(lines, "Run `roca vector compact` to reclaim empty embedding pages on "+strings.Join(compact, ", "))
	}
	if len(live) > 0 {
		lines = append(lines, "index.lock is held on "+strings.Join(live, ", "))
	}
	if len(stale) > 0 {
		lines = append(lines, "index.lock is stale on "+strings.Join(stale, ", ")+"; ingest and compact can take it")
	}
	return lines
}

func renderVectorization(report vector.Vectorization, help []string) string {
	columns := []string{"plugin", "database", "tables", "embedded_chunks", "candidate_chunks",
		"sidecar_bytes", "last_write", "state", "index_lock", "compact_recommended"}
	rows := make([]map[string]any, 0, len(report.Databases))
	for _, row := range report.Databases {
		lock := row.IndexLock
		if lock == "" {
			lock = "unknown"
		}
		rows = append(rows, map[string]any{
			"plugin":              row.Plugin,
			"database":            row.Database,
			"tables":              strings.Join(row.Tables, " "),
			"embedded_chunks":     nullableInt(row.EmbeddedChunks),
			"candidate_chunks":    nullableInt(row.CandidateChunks),
			"sidecar_bytes":       nullableInt(row.SidecarBytes),
			"last_write":          nullableString(row.LastWrite),
			"state":               row.State,
			"index_lock":          lock,
			"compact_recommended": row.CompactRecommended,
		})
	}
	var out strings.Builder
	out.WriteString("worker: ")
	out.WriteString(renderWorker(report.Worker))
	out.WriteByte('\n')
	out.WriteString(toonTable("databases", columns, rows))
	if rendered := renderHelp(help); rendered != "" {
		out.WriteByte('\n')
		out.WriteString(rendered)
	}
	return out.String()
}

func renderWorker(worker vector.WorkerStatus) string {
	running := "not running"
	if worker.Running {
		running = "running"
	}
	return strings.Join([]string{
		running,
		"pid " + unknownInt(worker.PID),
		"backend " + unknownString(worker.Backend),
		"database " + unknownString(worker.Database),
	}, " · ")
}

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func unknownInt(value *int) string {
	if value == nil {
		return "unknown"
	}
	return strconv.Itoa(*value)
}

func unknownString(value *string) string {
	if value == nil || *value == "" {
		return "unknown"
	}
	return *value
}

func toonTable(name string, columns []string, rows []map[string]any) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s[%d]{", name, len(rows))
	out.WriteString(strings.Join(columns, ","))
	out.WriteString("}:")
	for _, row := range rows {
		out.WriteString("\n  ")
		for i, column := range columns {
			if i > 0 {
				out.WriteByte(',')
			}
			out.WriteString(toonCell(row[column]))
		}
	}
	return out.String()
}

func toonCell(value any) string {
	if value == nil {
		return "null"
	}
	switch typed := value.(type) {
	case string:
		return toonString(typed)
	case int, int64, int32:
		return fmt.Sprint(typed)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return toonString(fmt.Sprint(typed))
	}
}

var toonNumber = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

func toonString(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || value == "true" || value == "false" || value == "null" ||
		strings.ContainsAny(value, ",:\"\\[]{}\n\r\t") || strings.Trim(value, " ") != value ||
		strings.HasPrefix(value, "-") || strings.HasPrefix(value, "#") || toonNumber.MatchString(value) {
		return quoteTOON(value)
	}
	return value
}

func quoteTOON(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			out.WriteString(`\\`)
		case '"':
			out.WriteString(`\"`)
		case '\n':
			out.WriteString(`\n`)
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}

func renderHelp(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "help[%d]:", len(lines))
	for _, line := range lines {
		out.WriteString("\n  - ")
		out.WriteString(toonString(line))
	}
	return out.String()
}

func printJSONTo(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
