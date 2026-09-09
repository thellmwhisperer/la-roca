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

func (s *Service) OpenExecCursor(ctx context.Context, sql string, maxChars int) (*ExecCursor, error) {
	route, statement, err := s.prepareExec(ctx, sql, true)
	if err != nil {
		return nil, err
	}
	connection, attached, err := s.openQueryConnection(ctx)
	if err != nil {
		route.CloseOnDemand()
		return nil, typedExecError(err)
	}
	close := func() { closeQueryConnection(connection, attached); route.CloseOnDemand() }
	var onDemand []plugin.Database
	for _, database := range route.Databases {
		if database.Semantic.Attachment != plugin.AttachmentResident {
			onDemand = append(onDemand, database)
		}
	}
	additional, err := plugin.Attach(ctx, connection, onDemand)
	attached = append(attached, additional...)
	if err != nil {
		close()
		return nil, typedExecError(err)
	}
	rows, err := connection.QueryContext(ctx, statement)
	if err != nil {
		close()
		return nil, typedExecError(err)
	}
	return &ExecCursor{rows: rows, close: close, maxChars: TextBudget(maxChars),
		databases: route.Databases, statement: statement}, nil
}

func (c *ExecCursor) ReadPage() ([]map[string]any, error) {
	columns, rows, err := scanRowsPage(c.rows, c.maxChars, "", 500)
	if err != nil {
		return nil, typedExecError(err)
	}
	if len(c.databases) > 0 {
		_, rows = EnsureDatabaseColumn(columns, rows, fallbackDatabase(c.statement, c.databases))
	}
	return rows, nil
}

func (c *ExecCursor) Close() {
	c.rows.Close()
	c.close()
}
