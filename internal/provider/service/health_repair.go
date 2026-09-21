package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const (
	healthRepairRewrote    = "rewrote"
	healthRepairDeleted    = "deleted"
	healthRepairRegistered = "registered"
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

func healthCheckByName(name string) (healthCheck, bool) {
	for _, check := range healthChecks {
		if check.name == name {
			return check, true
		}
	}
	return healthCheck{}, false
}

func healthRepairNames() []string {
	names := make([]string, 0, len(healthChecks))
	for _, check := range healthChecks {
		if check.remedy != "" {
			names = append(names, check.name)
		}
	}
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
	name = strings.TrimSpace(name)
	check, ok := healthCheckByName(name)
	if !ok || check.remedy == "" {
		return HealthRepairResult{}, unknownHealthRepair(name)
	}
	if _, err := s.EnsureSchema(ctx); err != nil {
		return HealthRepairResult{}, err
	}
	switch name {
	case "orphan_supersedes":
		return s.repairOrphanSupersedes(ctx)
	case "test_metadata_rows":
		return s.repairTestMetadataRows(ctx)
	case "test_source_agent_rows":
		return s.repairTestSourceAgentRows(ctx)
	case "physical_alias_layer_rows":
		return s.repairPhysicalAliasLayerRows(ctx)
	case "runtime_layers_not_in_registry":
		return s.repairRuntimeLayers(ctx)
	default:
		return HealthRepairResult{}, unknownHealthRepair(name)
	}
}

type healthRepairRow struct {
	id         int64
	fields     map[string]any
	updateSQL  string
	updateArgs []any
}

func (s *Service) repairMemoryRows(ctx context.Context, check, action, selectSQL string,
	selectArgs []any, apply func(id int64, fields map[string]any) (string, []any, error),
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
		columns, err := rows.Columns()
		if err != nil {
			return fmt.Errorf("list %s columns: %w", check, err)
		}
		var planned []healthRepairRow
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				return fmt.Errorf("read a %s row: %w", check, err)
			}
			fields := make(map[string]any, len(columns))
			var id int64
			for i, column := range columns {
				fields[column] = scanValue(values[i])
				if column == "id" {
					id, err = asInt64(values[i])
					if err != nil {
						return fmt.Errorf("read a %s id: %w", check, err)
					}
				}
			}
			statement, arguments, err := apply(id, fields)
			if err != nil {
				return err
			}
			planned = append(planned, healthRepairRow{
				id: id, fields: fields, updateSQL: statement, updateArgs: arguments,
			})
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("list %s rows: %w", check, err)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, item := range planned {
			if _, err := tx.ExecContext(ctx, item.updateSQL, item.updateArgs...); err != nil {
				return fmt.Errorf("%s id %d: %w", action, item.id, err)
			}
			result.Rows = append(result.Rows, item.fields)
		}
		result.Count = len(planned)
		return nil
	})
	return result, err
}

func (s *Service) repairOrphanSupersedes(ctx context.Context) (HealthRepairResult, error) {
	return s.repairMemoryRows(ctx, "orphan_supersedes", healthRepairRewrote,
		`SELECT id, supersedes FROM memories
		 WHERE supersedes IS NOT NULL
		   AND supersedes NOT IN (SELECT id FROM memories)
		 ORDER BY id`, nil,
		func(id int64, _ map[string]any) (string, []any, error) {
			return `UPDATE memories SET supersedes = NULL WHERE id = ?`, []any{id}, nil
		})
}

func (s *Service) repairTestMetadataRows(ctx context.Context) (HealthRepairResult, error) {
	return s.repairMemoryRows(ctx, "test_metadata_rows", healthRepairDeleted,
		`SELECT id, layer, source_agent FROM memories
		 WHERE json_valid(metadata)
		   AND json_extract(metadata, '$._test') IN (1, 'true', 'True')
		 ORDER BY id`, nil,
		func(id int64, _ map[string]any) (string, []any, error) {
			return `DELETE FROM memories WHERE id = ?`, []any{id}, nil
		})
}

func (s *Service) repairTestSourceAgentRows(ctx context.Context) (HealthRepairResult, error) {
	return s.repairMemoryRows(ctx, "test_source_agent_rows", healthRepairDeleted,
		`SELECT id, layer, source_agent FROM memories
		 WHERE source_agent IN ('test-agent', 'test')
		 ORDER BY id`, nil,
		func(id int64, _ map[string]any) (string, []any, error) {
			return `DELETE FROM memories WHERE id = ?`, []any{id}, nil
		})
}

func (s *Service) repairPhysicalAliasLayerRows(ctx context.Context) (HealthRepairResult, error) {
	registered, err := s.registeredLayers(ctx)
	if err != nil {
		return HealthRepairResult{}, err
	}
	check, ok := healthCheckByName("physical_alias_layer_rows")
	if !ok {
		return HealthRepairResult{}, unknownHealthRepair("physical_alias_layer_rows")
	}
	prefix, arguments := healthQuery(check, registered)
	return s.repairMemoryRows(ctx, "physical_alias_layer_rows", healthRepairRewrote,
		prefix+`SELECT m.id, m.layer, l.alias_of FROM memories m
		 JOIN layers l ON l.name = m.layer
		 WHERE l.alias_of IS NOT NULL
		 ORDER BY m.id`, arguments,
		func(id int64, fields map[string]any) (string, []any, error) {
			physical, _ := fields["alias_of"].(string)
			if physical == "" {
				return "", nil, fmt.Errorf("physical alias row %d names no destination", id)
			}
			return `UPDATE memories SET layer = ? WHERE id = ?`, []any{physical, id}, nil
		})
}

func (s *Service) repairRuntimeLayers(ctx context.Context) (HealthRepairResult, error) {
	names, err := s.unregisteredLayers(ctx)
	if err != nil {
		return HealthRepairResult{}, err
	}
	result := HealthRepairResult{Check: "runtime_layers_not_in_registry", Action: healthRepairRegistered}
	for _, name := range names {
		added, err := s.AddLayer(ctx, name)
		if err != nil {
			return result, err
		}
		if !added.Added {
			continue
		}
		result.Rows = append(result.Rows, map[string]any{"layer": name})
		result.Count++
	}
	return result, nil
}

func scanValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return string(typed)
	default:
		return typed
	}
}

func asInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case []byte:
		return 0, fmt.Errorf("id is not an integer")
	default:
		return 0, fmt.Errorf("id is not an integer")
	}
}
