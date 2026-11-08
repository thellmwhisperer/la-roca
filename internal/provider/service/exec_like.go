package service

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	rqlite "github.com/rqlite/sql"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

// This advisory uses the existing SQLite parser; unsupported syntax still goes
// through the engine's validation and execution bound.
func rejectLeadingWildcard(statement string, schema query.Schema) error {
	statements, err := rqlite.NewParser(strings.NewReader(statement)).ParseStatements()
	if err != nil {
		return nil
	}
	for _, stmt := range statements {
		if _, err := rqlite.Walk(likeVisitor{schema: schema}, stmt); err != nil {
			return err
		}
	}
	return nil
}

type likeVisitor struct {
	schema  query.Schema
	sources []rqlite.Source
}

func (v likeVisitor) VisitEnd(node rqlite.Node) (rqlite.Node, error) { return node, nil }

func (v likeVisitor) Visit(node rqlite.Node) (rqlite.Visitor, rqlite.Node, error) {
	if selectSQL, ok := node.(*rqlite.SelectStatement); ok {
		v.sources = rqlite.SourceList(selectSQL.Source)
	}
	comparison, ok := node.(*rqlite.BinaryExpr)
	if !ok || comparison.Op != rqlite.LIKE {
		return v, node, nil
	}
	pattern, ok := comparison.Y.(*rqlite.StringLit)
	if !ok || !strings.HasPrefix(pattern.Value, "%") {
		return v, node, nil
	}
	var column, qualifier string
	switch ref := comparison.X.(type) {
	case *rqlite.Ident:
		column = ref.Name
	case *rqlite.QualifiedRef:
		column, qualifier = ref.Column.Name, ref.Table.Name
	default:
		return v, node, nil
	}
	for _, source := range v.sources {
		table, ok := source.(*rqlite.QualifiedTableName)
		if !ok || table.Schema == nil {
			continue
		}
		if qualifier != "" && !strings.EqualFold(qualifier, table.Name.Name) &&
			(table.Alias == nil || !strings.EqualFold(qualifier, table.Alias.Name)) {
			continue
		}
		qualified := table.Schema.Name + "." + table.Name.Name
		twinName := qualified + "_fts"
		if strings.EqualFold(table.Name.Name, "thinking_blocks") {
			twinName = table.Schema.Name + ".thinking_fts"
		}
		for _, twin := range v.schema.Tables {
			name := twin.Name
			if !strings.Contains(name, ".") {
				name = "main." + name
			}
			if !twin.FTS5 || !strings.EqualFold(name, twinName) ||
				!slices.ContainsFunc(twin.Columns, func(name string) bool { return strings.EqualFold(name, column) }) {
				continue
			}
			fts := name[strings.LastIndex(name, ".")+1:]
			term := strings.Trim(pattern.Value, "%")
			// Quote FTS punctuation as a phrase, then quote SQL and the shell separately.
			if slices.Contains([]string{"OR", "AND", "NOT"}, term) ||
				strings.ContainsFunc(term, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) || term == "" {
				term = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
			}
			literal := (&rqlite.StringLit{Value: term}).String()
			columnMatch := (&rqlite.StringLit{Value: (&rqlite.Ident{Name: column}).String() + " : " + term}).String()
			suggestion := fmt.Sprintf("SELECT rowid AS id FROM %s.%s WHERE %s MATCH %s AND %s MATCH %s",
				table.Schema.String(), fts, fts, literal, fts, columnMatch)
			shellSQL := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(suggestion)
			return nil, node, fmt.Errorf("leading-wildcard LIKE on %s.%s.%s would scan text with an FTS twin; use MATCH for token search (not substring matching), or set --timeout-ms explicitly:\nroca exec \"%s\"\n",
				table.Schema.Name, table.Name.Name, column, shellSQL)
		}
	}
	return v, node, nil
}
