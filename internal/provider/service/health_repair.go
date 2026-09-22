package service

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

const (
	healthRepairRewrote            = "rewrote"
	healthRepairDeleted            = "deleted"
	testMetadataRepairPredicate    = "json_valid(m.metadata) AND json_extract(m.metadata, '$._test') IN (1, 'true', 'True')"
	testSourceAgentRepairPredicate = "m.source_agent IN ('test-agent', 'test')"
)

// HealthRepairResult is what one scoped health repair did. Count is the
// number of rows the named check owned; Rows is each one, so the operator
// can see exactly which rows the repair claimed.
type HealthRepairResult struct {
	Check  string           `json:"check"`
	Action string           `json:"action"`
	Count  int              `json:"count"`
	Rows   []map[string]any `json:"rows,omitempty"`
}

// healthRepairs is the one place that says which checks carry a scoped repair.
// A check whose remedy is a command somebody else already owns is absent here.
var healthRepairs = map[string]func(*Service, context.Context) (HealthRepairResult, error){
	"orphan_supersedes":              (*Service).repairOrphanSupersedes,
	"test_metadata_rows":             (*Service).repairTestMetadataRows,
	"test_source_agent_rows":         (*Service).repairTestSourceAgentRows,
	"runtime_layers_not_in_registry": (*Service).repairRuntimeLayersNotInRegistry,
	"physical_alias_layer_rows":      (*Service).repairPhysicalAliasLayerRows,
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
// prints. The diagnosed rows are the direct repair targets. Deleting a memory
// also rewires incoming supersedes pointers and retires exact-dedup aliases
// that named it as canonical.
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

// healthRepairStatement is one write a repair makes for one row.
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
	selectArgs []any, apply func(id int64, fields map[string]any, remapsPresent bool) ([]healthRepairStatement, error),
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
		var remapsPresent int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'memory_id_remaps'`).
			Scan(&remapsPresent); err != nil {
			return fmt.Errorf("inspect memory id remaps: %w", err)
		}
		planned := make([]healthRepairRow, 0, len(scanned))
		for _, fields := range scanned {
			id, ok := fields["id"].(int64)
			if !ok {
				return fmt.Errorf("a %s row has no integer id", check)
			}
			statements, err := apply(id, fields, remapsPresent != 0)
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

func deleteMemoryRow(id int64, remapsPresent bool, deletionPredicate string) ([]healthRepairStatement, error) {
	statements := []healthRepairStatement{
		{sql: `WITH RECURSIVE chain(id, candidate_id, depth, path) AS (
				SELECT id, supersedes, 0, printf(',%lld,', id)
				FROM memories WHERE id = ?
				UNION ALL
				SELECT chain.id, m.supersedes, chain.depth + 1,
					chain.path || printf('%lld,', m.id)
				FROM chain
				JOIN memories m ON m.id = chain.candidate_id
				WHERE (` + deletionPredicate + `)
				  AND instr(chain.path, printf(',%lld,', m.id)) = 0
			)
			UPDATE memories SET supersedes = (
				SELECT CASE WHEN EXISTS (
					SELECT 1 FROM memories survivor WHERE survivor.id = chain.candidate_id
				) THEN chain.candidate_id END
				FROM chain
				WHERE chain.candidate_id IS NULL OR NOT EXISTS (
					SELECT 1 FROM memories m
					WHERE m.id = chain.candidate_id AND (` + deletionPredicate + `)
				)
				ORDER BY chain.depth
				LIMIT 1
			) WHERE supersedes = ?`, args: []any{id, id}},
	}
	if remapsPresent {
		statements = append(statements,
			healthRepairStatement{sql: `DELETE FROM memory_id_remaps WHERE canonical_id = ?`, args: []any{id}})
	}
	return append(statements,
		healthRepairStatement{sql: `DELETE FROM memories WHERE id = ?`, args: []any{id}}), nil
}

func (s *Service) repairOrphanSupersedes(ctx context.Context) (HealthRepairResult, error) {
	return s.repairMemoryRows(ctx, "orphan_supersedes", healthRepairRewrote,
		`SELECT id, supersedes FROM memories
		 WHERE supersedes IS NOT NULL
		   AND supersedes NOT IN (SELECT id FROM memories)
		 ORDER BY id`, nil,
		func(id int64, _ map[string]any, _ bool) ([]healthRepairStatement, error) {
			return []healthRepairStatement{
				{sql: `UPDATE memories SET supersedes = NULL WHERE id = ?`, args: []any{id}},
			}, nil
		})
}

func (s *Service) repairTestMetadataRows(ctx context.Context) (HealthRepairResult, error) {
	return s.repairMemoryRows(ctx, "test_metadata_rows", healthRepairDeleted,
		`SELECT m.id, m.layer, m.source_agent FROM memories m
		 WHERE `+testMetadataRepairPredicate+`
		 ORDER BY m.id`, nil,
		func(id int64, _ map[string]any, remapsPresent bool) ([]healthRepairStatement, error) {
			return deleteMemoryRow(id, remapsPresent, testMetadataRepairPredicate)
		})
}

func (s *Service) repairTestSourceAgentRows(ctx context.Context) (HealthRepairResult, error) {
	return s.repairMemoryRows(ctx, "test_source_agent_rows", healthRepairDeleted,
		`SELECT id, layer, source_agent FROM memories
		 WHERE source_agent IN ('test-agent', 'test')
		 ORDER BY id`, nil,
		func(id int64, _ map[string]any, remapsPresent bool) ([]healthRepairStatement, error) {
			return deleteMemoryRow(id, remapsPresent, testSourceAgentRepairPredicate)
		})
}

func (s *Service) repairRuntimeLayersNotInRegistry(ctx context.Context) (HealthRepairResult, error) {
	names, err := s.unregisteredLayers(ctx)
	if err != nil {
		return HealthRepairResult{}, err
	}
	result := HealthRepairResult{
		Check:  "runtime_layers_not_in_registry",
		Action: "registered",
		Count:  len(names),
	}
	for _, name := range names {
		added, err := s.addLayerExact(ctx, name)
		if err != nil {
			return HealthRepairResult{}, err
		}
		result.Rows = append(result.Rows, map[string]any{
			"name":  added.Name,
			"added": added.Added,
		})
	}
	return result, nil
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
		func(id int64, fields map[string]any, _ bool) ([]healthRepairStatement, error) {
			physical, _ := fields["alias_of"].(string)
			if physical == "" {
				return nil, fmt.Errorf("physical alias row %d names no destination", id)
			}
			return []healthRepairStatement{
				{sql: `UPDATE memories SET layer = ? WHERE id = ?`, args: []any{physical, id}},
			}, nil
		})
}
