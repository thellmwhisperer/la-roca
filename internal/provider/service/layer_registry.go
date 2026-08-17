package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

// LayerAddResult reports whether a layer registration changed the catalogue.
type LayerAddResult struct {
	Name  string `json:"name"`
	Added bool   `json:"added"`
}

// LayerMigrateResult reports the physical destination and rows repaired.
type LayerMigrateResult struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Migrated int64  `json:"migrated"`
}

type registeredLayer struct {
	name    string
	aliasOf string
}

func (s *Service) memoryOwner() (*store.DB, error) {
	if !s.opts.RocaOpsEnabled {
		return s.db, nil
	}
	if s.ops == nil {
		return nil, fmt.Errorf("%s is unavailable for operational memory writes", rocaOpsPluginName)
	}
	return s.ops, nil
}

func (s *Service) memoryReader(ctx context.Context) (*sql.DB, func(), error) {
	if !s.opts.RocaOpsEnabled {
		reader, err := s.db.ReadOnly()
		return reader, func() {}, err
	}
	if s.ops != nil {
		reader, err := s.ops.ReadOnly()
		return reader, func() {}, err
	}
	database := databaseForVerb(s.resident, StoreVerb, rocaOpsPluginName)
	if database == nil {
		return nil, func() {}, fmt.Errorf("%s is unavailable for operational memory reads", rocaOpsPluginName)
	}
	reader, err := sql.Open("sqlite", database.ReadOnlyURI())
	if err != nil {
		return nil, func() {}, fmt.Errorf("open %s read-only: %w", rocaOpsPluginName, err)
	}
	if err := reader.PingContext(ctx); err != nil {
		reader.Close()
		return nil, func() {}, fmt.Errorf("open %s read-only: %w", rocaOpsPluginName, err)
	}
	return reader, func() { _ = reader.Close() }, nil
}

func (s *Service) layerOwner() (*store.DB, error) {
	if s.layerDB != nil {
		return s.layerDB, nil
	}
	if s.layerSet != nil {
		return nil, fmt.Errorf("%s layer registry is not open for writes", rocaOpsPluginName)
	}
	if s.db == nil {
		return nil, fmt.Errorf("no durable layer registry is available")
	}
	return s.db, nil
}

func (s *Service) layerReader(ctx context.Context) (*sql.DB, func(), error) {
	if s.layerDB != nil {
		reader, err := s.layerDB.ReadOnly()
		return reader, func() {}, err
	}
	if s.layerSet != nil {
		reader, err := sql.Open("sqlite", s.layerSet.ReadOnlyURI())
		if err != nil {
			return nil, func() {}, fmt.Errorf("open %s layer registry read-only: %w", rocaOpsPluginName, err)
		}
		if err := reader.PingContext(ctx); err != nil {
			reader.Close()
			return nil, func() {}, fmt.Errorf("open %s layer registry read-only: %w", rocaOpsPluginName, err)
		}
		return reader, func() { _ = reader.Close() }, nil
	}
	reader, err := s.db.ReadOnly()
	return reader, func() {}, err
}

// registeredLayers reads the live catalogue. The table, rather than the
// embedded defaults, is authoritative because operators may intentionally add
// a layer after installation.
func (s *Service) registeredLayers(ctx context.Context) ([]registeredLayer, error) {
	reader, closeReader, err := s.layerReader(ctx)
	if err != nil {
		return nil, err
	}
	defer closeReader()
	rows, err := reader.QueryContext(ctx,
		`SELECT name, COALESCE(alias_of, '') FROM layers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("read the layer registry: %w", err)
	}
	defer rows.Close()

	var registered []registeredLayer
	for rows.Next() {
		var layer registeredLayer
		if err := rows.Scan(&layer.name, &layer.aliasOf); err != nil {
			return nil, fmt.Errorf("read the layer registry: %w", err)
		}
		registered = append(registered, layer)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the layer registry: %w", err)
	}
	return registered, nil
}

func (s *Service) resolveRegisteredLayer(ctx context.Context, requested string) (string, error) {
	registered, err := s.registeredLayers(ctx)
	if err != nil {
		return "", err
	}
	aliases := make(map[string]string, len(registered))
	names := make([]string, 0, len(registered))
	for _, layer := range registered {
		aliases[layer.name] = layer.aliasOf
		names = append(names, layer.name)
	}
	if _, ok := aliases[requested]; !ok {
		listed := "(none)"
		if len(names) > 0 {
			listed = strings.Join(names, ", ")
		}
		return "", fmt.Errorf("layer %q is not registered; registered layers: %s", requested, listed)
	}

	physical := requested
	for range len(registered) {
		alias, ok := aliases[physical]
		if !ok || alias == "" {
			return physical, nil
		}
		physical = alias
	}
	return physical, nil
}

// AddLayer intentionally registers one exact layer name. Existing entries are
// left untouched, including the embedded entries maintained by syncLayers.
func (s *Service) AddLayer(ctx context.Context, name string) (LayerAddResult, error) {
	if s.opts.ReadOnly {
		return LayerAddResult{}, refuseReadOnly("add layer")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return LayerAddResult{}, fmt.Errorf("a layer name is required")
	}
	if _, err := s.ensureSchema(ctx); err != nil {
		return LayerAddResult{}, err
	}
	owner, err := s.layerOwner()
	if err != nil {
		return LayerAddResult{}, err
	}

	result := LayerAddResult{Name: name}
	err = owner.Write(ctx, func(tx *sql.Tx) error {
		outcome, err := tx.ExecContext(ctx, `INSERT INTO layers
			(name, description, schema_file, ingest_allowed, added_by, lifecycle, since_version)
			VALUES (?, 'Operator-registered memory layer.', '', 1, 'operator', 'curated', ?)
			ON CONFLICT(name) DO NOTHING`, name, s.opts.Version)
		if err != nil {
			return fmt.Errorf("register layer %q: %w", name, err)
		}
		changed, err := outcome.RowsAffected()
		result.Added = changed > 0
		return err
	})
	return result, err
}

// MigrateLayer moves memories from one exact layer spelling to a registered
// physical destination in the database selected by this service.
func (s *Service) MigrateLayer(ctx context.Context, from, to string) (LayerMigrateResult, error) {
	if s.opts.ReadOnly {
		return LayerMigrateResult{}, refuseReadOnly("migrate layer")
	}
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" {
		return LayerMigrateResult{}, fmt.Errorf("both source and destination layers are required")
	}
	if _, err := s.ensureSchema(ctx); err != nil {
		return LayerMigrateResult{}, err
	}
	physical, err := s.resolveRegisteredLayer(ctx, to)
	if err != nil {
		return LayerMigrateResult{}, err
	}
	owner, err := s.memoryOwner()
	if err != nil {
		return LayerMigrateResult{}, err
	}

	result := LayerMigrateResult{From: from, To: physical}
	err = owner.Write(ctx, func(tx *sql.Tx) error {
		outcome, err := tx.ExecContext(ctx,
			`UPDATE memories SET layer = ? WHERE layer = ?`, physical, from)
		if err != nil {
			return fmt.Errorf("migrate memories from layer %q to %q: %w", from, physical, err)
		}
		result.Migrated, err = outcome.RowsAffected()
		return err
	})
	return result, err
}

func (s *Service) unregisteredLayers(ctx context.Context) ([]string, error) {
	registered, err := s.registeredLayers(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(registered))
	for _, layer := range registered {
		known[layer.name] = struct{}{}
	}
	reader, closeReader, err := s.memoryReader(ctx)
	if err != nil {
		return nil, err
	}
	defer closeReader()
	rows, err := reader.QueryContext(ctx, `SELECT layer FROM memories GROUP BY layer ORDER BY layer`)
	if err != nil {
		return nil, fmt.Errorf("find runtime layers absent from the registry: %w", err)
	}
	defer rows.Close()
	var unregistered []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("find runtime layers absent from the registry: %w", err)
		}
		if _, ok := known[name]; !ok {
			unregistered = append(unregistered, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find runtime layers absent from the registry: %w", err)
	}
	return unregistered, nil
}
