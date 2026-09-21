package service

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

const (
	healthRepairRewrote = "rewrote"
	healthRepairDeleted = "deleted"
)

// HealthRepairResult is what one scoped health repair did. Count is the
// number of rows the named check owned; Rows is each one, so the operator
// can see that nothing else moved.
type HealthRepairResult struct {
	Check  string           `json:"check"`
	Action string           `json:"action"`
	Count  int              `json:"count"`
	Rows   []map[string]any `json:"rows,omitempty"`
}

// healthRepairs is the one place that says which checks carry a scoped repair.
// A check whose remedy is a command somebody else already owns is absent here.
var healthRepairs = map[string]func(*Service, context.Context) (HealthRepairResult, error){
	"orphan_supersedes":         (*Service).repairOrphanSupersedes,
	"test_metadata_rows":        (*Service).repairTestMetadataRows,
	"test_source_agent_rows":    (*Service).repairTestSourceAgentRows,
	"physical_alias_layer_rows": (*Service).repairPhysicalAliasLayerRows,
}

func healthRepairNames() []string {
	names := make([]string, 0, len(healthRepairs))
	for name := range healthRepairs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func unknownHealthRepair(name string) error {
	return fmt.Errorf("unknown health repair %q; known: %s", name, strings.Join(healthRepairNames(), ", "))
}

// RepairHealth applies the scoped repair the named failing health check
// prints. It rewrites or deletes only the rows that check names.
func (s *Service) RepairHealth(ctx context.Context, name string) (HealthRepairResult, error) {
	if s.opts.ReadOnly {
		return HealthRepairResult{}, refuseReadOnly("repair health")
	}
	repair, ok := healthRepairs[strings.TrimSpace(name)]
	if !ok {
		return HealthRepairResult{}, unknownHealthRepair(name)
	}
	if _, err := s.EnsureSchema(ctx); err != nil {
		return HealthRepairResult{}, err
	}
	return repair(s, ctx)
}

// healthRepairStatement is one write a repair makes for one row. A repair that
// deletes needs two: memories.supersedes points at memories.id, so the row has
// to stop being referenced before it can go.
type healthRepairStatement struct {
	sql  string
	args []any
}

type healthRepairRow struct {
	id         int64
	fields     map[string]any
	statements []healthRepairStatement
}

func (s *Service) repairMemoryRows(ctx context.Context, check, action, selectSQL string,
	selectArgs []any, apply func(id int64, fields map[string]any) ([]healthRepairStatement, error),
) (HealthRepairResult, error) {
	owner, err := s.memoryOwner()
	if err != nil {
		return HealthRepairResult{}, err
	}
	result := HealthRepairResult{Check: check, Action: action}
	err = owner.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, selectSQL, selectArgs...)
		if err != nil {
			return fmt.Errorf("list %s rows: %w", check, err)
		}
		defer rows.Close()
		_, scanned, err := ScanRows(rows, 0, "")
		if err != nil {
			return fmt.Errorf("list %s rows: %w", check, err)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		planned := make([]healthRepairRow, 0, len(scanned))
		for _, fields := range scanned {
			id, ok := fields["id"].(int64)
			if !ok {
				return fmt.Errorf("a %s row has no integer id", check)
			}
			statements, err := apply(id, fields)
			if err != nil {
				return err
			}
			planned = append(planned, healthRepairRow{id: id, fields: fields, statements: statements})
		}
		for _, item := range planned {
			for _, statement := range item.statements {
				if _, err := tx.ExecContext(ctx, statement.sql, statement.args...); err != nil {
					return fmt.Errorf("%s id %d: %w", action, item.id, err)
				}
			}
			result.Rows = append(result.Rows, item.fields)
		}
		result.Count = len(planned)
		return nil
	})
	return result, err
}

func deleteMemoryRow(id int64) ([]healthRepairStatement, error) {
	return []healthRepairStatement{
		{sql: `UPDATE memories SET supersedes = NULL WHERE supersedes = ?`, args: []any{id}},
		{sql: `DELETE FROM memories WHERE id = ?`, args: []any{id}},
	}, nil
}

func (s *Service) repairOrphanSupersedes(ctx context.Context) (HealthRepairResult, error) {
	return s.repairMemoryRows(ctx, "orphan_supersedes", healthRepairRewrote,
		`SELECT id, supersedes FROM memories
		 WHERE supersedes IS NOT NULL
		   AND supersedes NOT IN (SELECT id FROM memories)
		 ORDER BY id`, nil,
		func(id int64, _ map[string]any) ([]healthRepairStatement, error) {
			return []healthRepairStatement{
				{sql: `UPDATE memories SET supersedes = NULL WHERE id = ?`, args: []any{id}},
			}, nil
		})
}

func (s *Service) repairTestMetadataRows(ctx context.Context) (HealthRepairResult, error) {
	return s.repairMemoryRows(ctx, "test_metadata_rows", healthRepairDeleted,
		`SELECT id, layer, source_agent FROM memories
		 WHERE json_valid(metadata)
		   AND json_extract(metadata, '$._test') IN (1, 'true', 'True')
		 ORDER BY id`, nil,
		func(id int64, _ map[string]any) ([]healthRepairStatement, error) {
			return deleteMemoryRow(id)
		})
}

func (s *Service) repairTestSourceAgentRows(ctx context.Context) (HealthRepairResult, error) {
	return s.repairMemoryRows(ctx, "test_source_agent_rows", healthRepairDeleted,
		`SELECT id, layer, source_agent FROM memories
		 WHERE source_agent IN ('test-agent', 'test')
		 ORDER BY id`, nil,
		func(id int64, _ map[string]any) ([]healthRepairStatement, error) {
			return deleteMemoryRow(id)
		})
}

func (s *Service) repairPhysicalAliasLayerRows(ctx context.Context) (HealthRepairResult, error) {
	registered, err := s.registeredLayers(ctx)
	if err != nil {
		return HealthRepairResult{}, err
	}
	prefix, arguments := layerRegistryCTE(registered)
	return s.repairMemoryRows(ctx, "physical_alias_layer_rows", healthRepairRewrote,
		prefix+`SELECT m.id, m.layer, l.alias_of FROM memories m
		 JOIN layers l ON l.name = m.layer
		 WHERE l.alias_of IS NOT NULL
		 ORDER BY m.id`, arguments,
		func(id int64, fields map[string]any) ([]healthRepairStatement, error) {
			physical, _ := fields["alias_of"].(string)
			if physical == "" {
				return nil, fmt.Errorf("physical alias row %d names no destination", id)
			}
			return []healthRepairStatement{
				{sql: `UPDATE memories SET layer = ? WHERE id = ?`, args: []any{physical, id}},
			}, nil
		})
}
