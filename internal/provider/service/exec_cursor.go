package service

import (
	"context"
	"database/sql"
	"slices"

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
	service    *Service
	connection *sql.Conn
	attached   []string
	onDemand   []string
	active     int
}

func (s *Service) NewExecReader() *ExecReader {
	return &ExecReader{service: s}
}

func (r *ExecReader) Close() {
	if r.connection != nil {
		closeQueryConnection(r.connection, append(r.attached, r.onDemand...))
		r.connection = nil
	}
}

func (r *ExecReader) releaseAttachments() {
	if r.active == 0 && r.connection != nil {
		plugin.Detach(context.Background(), r.connection, r.onDemand)
		r.onDemand = nil
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
	if r.connection == nil {
		var err error
		r.connection, r.attached, err = r.service.openQueryConnection(ctx)
		if err != nil {
			return nil, typedExecError(err)
		}
	}
	var onDemand []plugin.Database
	for _, database := range databases {
		if database.Semantic.Attachment != plugin.AttachmentResident && !slices.Contains(r.onDemand, database.Schema) {
			onDemand = append(onDemand, database)
		}
	}
	additional, err := plugin.Attach(ctx, r.connection, onDemand)
	r.onDemand = append(r.onDemand, additional...)
	if err != nil {
		r.releaseAttachments()
		return nil, typedExecError(err)
	}
	rows, err := r.connection.QueryContext(ctx, statement)
	if err != nil {
		r.releaseAttachments()
		return nil, typedExecError(err)
	}
	r.active++
	close := func() { r.active--; r.releaseAttachments() }
	return &ExecCursor{rows: rows, close: close, maxChars: TextBudget(maxChars),
		databases: databases, statement: statement}, nil
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
	c.rows.Close()
	c.close()
}
