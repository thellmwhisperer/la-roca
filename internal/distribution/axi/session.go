package axi

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// Pills renders the pill roster with full content. It does not use RowOutput:
// that renderer clips every field, and a pill that does not arrive whole is
// not a pill.
func Pills(list service.PillList) string {
	var b strings.Builder
	appendLine(&b, "project: "+fullToonString(list.Project))
	appendLine(&b, fullRecords("pills", memoryColumns(), memoryRows(list.Pills)))
	if len(list.Unslugged) > 0 {
		appendLine(&b, unsluggedLine(list.Unslugged))
	}
	if len(list.Pills) == 0 {
		appendLine(&b, "no active pills")
	}
	appendLine(&b, RenderHelp(
		"Run `roca pill show <slug>` to load one pill",
		"Run `roca pill --json` for the complete envelope",
	))
	return b.String()
}

// Pill renders one complete pill.
func Pill(record service.MemoryRecord) string {
	return Pills(service.PillList{Project: record.Project, Pills: []service.MemoryRecord{record}})
}

// Handoffs renders active unsuperseded handoffs with full content.
func Handoffs(list service.HandoffList) string {
	var b strings.Builder
	appendLine(&b, "project: "+fullToonString(list.Project))
	if list.GlobalFallback {
		appendLine(&b, "fallback: global")
	}
	appendLine(&b, fullRecords("handoffs", memoryColumns(), memoryRows(list.Handoffs)))
	if len(list.Handoffs) == 0 {
		appendLine(&b, "no active handoff")
	}
	appendLine(&b, RenderHelp(
		"Run `roca handoff latest --json` for the complete envelope",
	))
	return b.String()
}

func memoryColumns() []string {
	return []string{"slug", "id", "project", "created_at", "content"}
}

func memoryRows(records []service.MemoryRecord) []map[string]any {
	rows := make([]map[string]any, 0, len(records))
	for _, record := range records {
		row := map[string]any{
			"id": record.ID, "created_at": record.CreatedAt, "content": record.Content,
		}
		if record.Slug != "" {
			row["slug"] = record.Slug
		}
		if record.Project != "" {
			row["project"] = record.Project
		}
		rows = append(rows, row)
	}
	return rows
}

func fullRecords(name string, columns []string, rows []map[string]any) string {
	if len(rows) == 0 {
		return fmt.Sprintf("%s[0]:", name)
	}
	order := presentColumns(columns, rows)
	var out strings.Builder
	fmt.Fprintf(&out, "%s[%d]{", name, len(rows))
	for i, column := range order {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(toonKey(column))
	}
	out.WriteString("}:")
	for _, row := range rows {
		out.WriteString("\n  ")
		for i, column := range order {
			if i > 0 {
				out.WriteByte(',')
			}
			out.WriteString(fullToonValue(row[column]))
		}
	}
	return out.String()
}

func presentColumns(columns []string, rows []map[string]any) []string {
	present := map[string]bool{}
	for _, row := range rows {
		for column := range row {
			present[column] = true
		}
	}
	order := make([]string, 0, len(columns))
	for _, column := range columns {
		if present[column] {
			order = append(order, column)
		}
	}
	return order
}

func fullToonValue(value any) string {
	if value == nil {
		return "null"
	}
	switch v := value.(type) {
	case string:
		return fullToonString(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	default:
		return fullToonString(fmt.Sprint(v))
	}
}

func fullToonString(value string) string {
	if !toonNeedsQuotes(value) {
		return value
	}
	return quoteTOON(value)
}

func unsluggedLine(ids []int64) string {
	var out strings.Builder
	fmt.Fprintf(&out, "unslugged[%d]:", len(ids))
	for i, id := range ids {
		if i > 0 {
			out.WriteString(",")
		}
		out.WriteByte(' ')
		out.WriteString(strconv.FormatInt(id, 10))
	}
	return out.String()
}
