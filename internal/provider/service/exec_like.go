package service

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	rqlite "github.com/rqlite/sql"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

// Diagnose only a timed-out SELECT; successful statements never pay for this hint.
func leadingWildcardHint(statement string, schema query.Schema) error {
	stmt, err := rqlite.NewParser(strings.NewReader(statement)).ParseStatement()
	if err == nil {
		_, err = rqlite.Walk(likeVisitor{schema: schema}, stmt)
		return err
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
	operand := unparenLikeOperand(comparison.Y)
	if escape, ok := operand.(*rqlite.BinaryExpr); ok && escape.Op == rqlite.ESCAPE {
		if char, ok := unparenLikeOperand(escape.Y).(*rqlite.StringLit); ok && char.Value == "%" {
			return v, node, nil
		}
		operand = unparenLikeOperand(escape.X)
	}
	pattern, ok := operand.(*rqlite.StringLit)
	if !ok || !strings.HasPrefix(pattern.Value, "%") {
		return v, node, nil
	}
	var column, qualifier string
	var qualifierSchema *rqlite.Ident
	switch ref := unparenLikeOperand(comparison.X).(type) {
	case *rqlite.Ident:
		column = ref.Name
	case *rqlite.QualifiedRef:
		column, qualifier, qualifierSchema = ref.Column.Name, ref.Table.Name, ref.Schema
	default:
		return v, node, nil
	}
	for _, source := range v.sources {
		table, ok := source.(*rqlite.QualifiedTableName)
		if !ok || table.Schema == nil {
			continue
		}
		if qualifierSchema != nil && !strings.EqualFold(qualifierSchema.Name, table.Schema.Name) {
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
			return nil, node, fmt.Errorf("leading-wildcard LIKE on %s.%s.%s scanned text with an FTS twin; use MATCH for token search (not substring matching), or set --timeout-ms explicitly:\nroca exec \"%s\"\n",
				table.Schema.Name, table.Name.Name, column, shellSQL)
		}
	}
	return v, node, nil
}

func unparenLikeOperand(expr rqlite.Expr) rqlite.Expr {
	for {
		paren, ok := expr.(*rqlite.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}
