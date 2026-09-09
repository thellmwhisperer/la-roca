package service

import (
	"context"
	"database/sql"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

// ExecCursor is a gated SELECT whose rows are consumed in bounded pages by the
// vector reader. The SELECT, sort and read connection are opened only once.
type ExecCursor struct {
	rows      *sql.Rows
	close     func()
	maxChars  int
	databases []plugin.Database
	statement string
}

type ExecReader struct {
	service *Service
	cursors map[*ExecCursor]struct{}
}

func (s *Service) NewExecReader() *ExecReader {
	return &ExecReader{service: s, cursors: make(map[*ExecCursor]struct{})}
}

func (r *ExecReader) Close() {
	for cursor := range r.cursors {
		cursor.Close()
	}
}

func (r *ExecReader) OpenExecCursor(ctx context.Context, sql string, maxChars int) (*ExecCursor, error) {
	route, statement, err := r.service.prepareExec(ctx, sql, true)
	if err != nil {
		return nil, err
	}
	defer route.CloseOnDemand()
	return r.open(ctx, statement, maxChars, route.Databases)
}

func (r *ExecReader) open(ctx context.Context, statement string, maxChars int, databases []plugin.Database) (*ExecCursor, error) {
	// A cursor owns its connection and attachments. Independent source sweeps
	// may interleave in the same helper without accumulating each other's seats.
	connection, attached, err := r.service.openQueryConnection(ctx)
	if err != nil {
		return nil, typedExecError(err)
	}
	var onDemand []plugin.Database
	for _, database := range databases {
		if database.Semantic.Attachment != plugin.AttachmentResident {
			onDemand = append(onDemand, database)
		}
	}
	additional, err := plugin.Attach(ctx, connection, onDemand)
	attached = append(attached, additional...)
	if err != nil {
		closeQueryConnection(connection, attached)
		return nil, typedExecError(err)
	}
	rows, err := connection.QueryContext(ctx, statement)
	if err != nil {
		closeQueryConnection(connection, attached)
		return nil, typedExecError(err)
	}
	cursor := &ExecCursor{rows: rows, maxChars: TextBudget(maxChars),
		databases: databases, statement: statement}
	cursor.close = func() {
		closeQueryConnection(connection, attached)
		delete(r.cursors, cursor)
	}
	r.cursors[cursor] = struct{}{}
	return cursor, nil
}

func (r *ExecReader) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	return r.service.exec(ctx, req, r)
}

func (r *ExecReader) execute(ctx context.Context, statement string, maxChars int, databases []plugin.Database, budget execBudget) ([]string, []map[string]any, error) {
	timeout, bounded := r.service.queryExecutionBudget()
	if budget.set {
		timeout, bounded = budget.timeout, budget.timeout > 0
	}
	queryCtx := ctx
	if bounded {
		var cancel context.CancelFunc
		queryCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cursor, err := r.open(queryCtx, statement, maxChars, databases)
	if err != nil {
		return nil, nil, executionError(ctx, queryCtx, timeout, err)
	}
	defer cursor.Close()
	columns, rows, err := cursor.readPage(0)
	if err != nil {
		return nil, nil, executionError(ctx, queryCtx, timeout, err)
	}
	return columns, rows, nil
}

func (c *ExecCursor) ReadPage() ([]map[string]any, error) {
	_, rows, err := c.readPage(500)
	return rows, err
}

func (c *ExecCursor) readPage(limit int) ([]string, []map[string]any, error) {
	columns, rows, err := scanRowsPage(c.rows, c.maxChars, "", limit)
	if err != nil {
		return nil, nil, typedExecError(err)
	}
	if len(c.databases) > 0 {
		columns, rows = EnsureDatabaseColumn(columns, rows, fallbackDatabase(c.statement, c.databases))
	}
	return columns, rows, nil
}

func (c *ExecCursor) Close() {
	if c.close != nil {
		c.rows.Close()
		c.close()
		c.close = nil
	}
}
