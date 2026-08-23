package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
)

// Live foreign SQLite sources are opened without mode=ro so SQLite can read
// their WAL shared indexes. The engine-level query_only guard still prevents
// writes, and the short busy timeout avoids blocking the owner.

// foreignBusyTimeoutMS is a quarter of a second. It is deliberately short: this
// process is a guest in somebody else's database, and a guest that waits fifteen
// seconds for a write lock is a guest that has stopped being read-only in effect.
const foreignBusyTimeoutMS = 250

// openForeign opens another agent's live SQLite database as a guest: query_only
// prevents writes and the 250 ms busy timeout prevents blocking its owner.
//
// Technical note: this is a normal OS-level open because SQLite mode=ro cannot
// reliably read a live WAL database without access to its shared index. The
// engine-level query_only guard rejects every write statement on the connection.
func openForeign(path string) (*sql.DB, error) {
	return openForeignPath(context.Background(), path, false)
}

func openForeignPath(ctx context.Context, path string, readOnly bool) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", path, err)
	}
	dsn := "file:" + abs + "?" + foreignSourceQuery(readOnly)
	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %q for reading: %w", abs, err)
	}
	if err := handle.PingContext(ctx); err != nil {
		handle.Close()
		return nil, fmt.Errorf("open %q for reading: %w", abs, err)
	}
	return handle, nil
}

func foreignSourceQuery(readOnly bool) string {
	query := url.Values{
		"_pragma": {
			fmt.Sprintf("busy_timeout(%d)", foreignBusyTimeoutMS),
			"query_only(1)",
		},
	}
	if readOnly {
		query.Set("mode", "ro")
	}
	return query.Encode()
}

// foreignTable is one table a source's database has to have, and the columns
// this build reads out of it.
type foreignTable struct {
	name     string
	required []string
}

// openForeignSource opens another agent's database and refuses it whole when its
// shape is not the one this build reads.
//
// The live-store adapters open their databases the same way and owe the operator
// the same refusal, so the sequence is written once and each of them brings only
// its own schema. `source` names the agent, because "the table is missing a
// column" without a name does not say which agent migrated under us.
//
// The schema is a slice and not a map on purpose: a map would name a different
// missing table on every run, and two runs of one broken database have to read
// the same.
func openForeignSource(ctx context.Context, source, path string, schema []foreignTable) (*sql.DB, error) {
	db, err := openForeign(path)
	if err != nil {
		return nil, err
	}
	for _, table := range schema {
		if err := requireColumns(ctx, db, table.name, table.required); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", source, err)
		}
	}
	return db, nil
}

// row is one foreign row read by column name. The foreign schemas grow columns
// between versions, and a reader that declared them all would break on the
// version that added one.
type row map[string]any

func (r row) text(key string) string {
	switch value := r[key].(type) {
	case string:
		return value
	case []byte:
		return string(value)
	}
	return ""
}

func (r row) number(key string) (float64, bool) {
	switch value := r[key].(type) {
	case int64:
		return float64(value), true
	case float64:
		return value, true
	case string:
		var parsed float64
		if _, err := fmt.Sscan(value, &parsed); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func (r row) has(key string) bool {
	value, present := r[key]
	return present && value != nil
}

// queryRows reads a whole result set by column name.
func queryRows(ctx context.Context, db *sql.DB, statement string, args ...any) ([]row, error) {
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []row
	for rows.Next() {
		cells := make([]any, len(columns))
		for i := range cells {
			cells[i] = new(any)
		}
		if err := rows.Scan(cells...); err != nil {
			return nil, err
		}
		record := row{}
		for i, name := range columns {
			record[name] = *(cells[i].(*any))
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// tableColumns is what a foreign table actually has, which is how a missing
// column becomes an absence to work around instead of a crash.
func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := queryRows(ctx, db, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	columns := map[string]bool{}
	for _, record := range rows {
		columns[record.text("name")] = true
	}
	return columns, nil
}

// requireColumns refuses to read a foreign table whose shape this build does not
// know. Reading it anyway would produce rows nobody can trust, and a named
// refusal is what tells the operator which agent changed its schema.
func requireColumns(ctx context.Context, db *sql.DB, table string, required []string) error {
	columns, err := tableColumns(ctx, db, table)
	if err != nil {
		return err
	}
	var missing []string
	for _, name := range required {
		if !columns[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the table %q is missing the columns this build reads: %v",
			table, missing)
	}
	return nil
}
