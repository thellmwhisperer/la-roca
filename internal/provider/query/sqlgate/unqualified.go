package sqlgate

import (
	"fmt"
	"strings"

	rqlite "github.com/rqlite/sql"
)

// RejectUnqualified refuses a statement that names a table without a schema
// when two or more attached databases expose that same name. The candidate
// list is the attached schemas themselves, in attach order. One attached
// match still resolves to core, which is how a corpus-only install keeps
// reading unqualified core memories. Zero matches leaves Validate to speak.
func RejectUnqualified(stmt string, schemas []Schema) error {
	if len(schemas) == 0 {
		return nil
	}
	for _, name := range unqualifiedTableNames(stmt) {
		candidates := qualifiedCandidates(name, schemas)
		if len(candidates) < 2 {
			continue
		}
		return fmt.Errorf("unqualified table %q; candidates: %s", name, strings.Join(candidates, ", "))
	}
	return nil
}

func (g *Gate) RejectUnqualified(stmt string) error {
	if g == nil {
		return nil
	}
	return RejectUnqualified(stmt, g.schemas)
}

func qualifiedCandidates(name string, schemas []Schema) []string {
	want := strings.ToLower(name)
	var candidates []string
	seen := map[string]bool{}
	for _, schema := range schemas {
		if schema.Name == "" || strings.EqualFold(schema.Name, "main") ||
			strings.EqualFold(schema.Name, "temp") {
			continue
		}
		for _, table := range schema.Tables {
			if IsHiddenTable(table.Name) || !strings.EqualFold(table.Name, want) {
				continue
			}
			candidate := schema.Name + "." + table.Name
			key := strings.ToLower(candidate)
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func unqualifiedTableNames(stmt string) []string {
	statements, err := rqlite.NewParser(strings.NewReader(stmt)).ParseStatements()
	if err != nil || len(statements) != 1 {
		return nil
	}
	sel, ok := statements[0].(*rqlite.SelectStatement)
	if !ok {
		return nil
	}
	collector := nameCollector{ctes: map[string]bool{}, seen: map[string]bool{}}
	collector.selectStmt(sel)
	return collector.names
}

type nameCollector struct {
	ctes  map[string]bool
	seen  map[string]bool
	names []string
}

func (c *nameCollector) selectStmt(sel *rqlite.SelectStatement) {
	if sel == nil {
		return
	}
	if sel.WithClause != nil {
		for _, cte := range sel.WithClause.CTEs {
			if name := strings.ToLower(rqlite.IdentName(cte.TableName)); name != "" {
				c.ctes[name] = true
			}
		}
		for _, cte := range sel.WithClause.CTEs {
			c.selectStmt(cte.Select)
		}
	}
	c.source(sel.Source)
	for _, col := range sel.Columns {
		if col != nil {
			c.expr(col.Expr)
		}
	}
	c.expr(sel.WhereExpr)
	for _, expr := range sel.GroupByExprs {
		c.expr(expr)
	}
	c.expr(sel.HavingExpr)
	c.selectStmt(sel.Compound)
}

func (c *nameCollector) source(src rqlite.Source) {
	switch s := src.(type) {
	case *rqlite.QualifiedTableName:
		c.table(s)
	case *rqlite.JoinClause:
		c.source(s.X)
		c.source(s.Y)
		if on, ok := s.Constraint.(*rqlite.OnConstraint); ok {
			c.expr(on.X)
		}
	case *rqlite.ParenSource:
		c.source(s.X)
	case *rqlite.SelectStatement:
		c.selectStmt(s)
	}
}

func (c *nameCollector) table(tbl *rqlite.QualifiedTableName) {
	if tbl == nil {
		return
	}
	schema := rqlite.IdentName(tbl.Schema)
	if schema != "" && !strings.EqualFold(schema, "main") {
		return
	}
	name := rqlite.IdentName(tbl.Name)
	if name == "" || c.ctes[strings.ToLower(name)] {
		return
	}
	key := strings.ToLower(name)
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.names = append(c.names, name)
}

func (c *nameCollector) expr(expr rqlite.Expr) {
	switch e := expr.(type) {
	case rqlite.SelectExpr:
		c.selectStmt(e.SelectStatement)
	case *rqlite.Exists:
		c.selectStmt(e.Select)
	case *rqlite.BinaryExpr:
		c.expr(e.X)
		c.expr(e.Y)
	case *rqlite.UnaryExpr:
		c.expr(e.X)
	case *rqlite.ParenExpr:
		c.expr(e.X)
	case *rqlite.Call:
		if e == nil {
			return
		}
		for _, arg := range e.Args {
			c.expr(arg)
		}
	case *rqlite.CaseExpr:
		if e == nil {
			return
		}
		c.expr(e.Operand)
		c.expr(e.ElseExpr)
		for _, block := range e.Blocks {
			if block != nil {
				c.expr(block.Condition)
				c.expr(block.Body)
			}
		}
	case *rqlite.CastExpr:
		if e != nil {
			c.expr(e.X)
		}
	case *rqlite.CollateExpr:
		if e != nil {
			c.expr(e.X)
		}
	case *rqlite.ExprList:
		if e == nil {
			return
		}
		for _, item := range e.Exprs {
			c.expr(item)
		}
	}
}
