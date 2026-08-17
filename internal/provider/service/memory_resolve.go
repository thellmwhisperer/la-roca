package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type MemoryResolution struct {
	RequestedID int64  `json:"requested_id"`
	CanonicalID int64  `json:"canonical_id"`
	Alias       bool   `json:"alias"`
	Database    string `json:"database"`
	Layer       string `json:"layer"`
	Content     string `json:"content"`
}

func (s *Service) ResolveMemory(ctx context.Context, requested int64) (MemoryResolution, error) {
	if requested <= 0 {
		return MemoryResolution{}, fmt.Errorf("a positive memory id is required")
	}
	type source struct {
		name string
		db   *sql.DB
	}
	var sources []source
	if s.ops != nil {
		sources = append(sources, source{name: rocaOpsPluginName, db: s.ops.SQL()})
	}
	if s.db != nil {
		sources = append(sources, source{name: "core", db: s.db.SQL()})
	}
	if s.corpus != nil {
		sources = append(sources, source{name: "plugin:roca-corpus", db: s.corpus.SQL()})
	}
	for _, source := range sources {
		resolved, found, err := resolveMemoryIn(ctx, source.db, source.name, requested)
		if err != nil {
			return MemoryResolution{}, err
		}
		if found {
			return resolved, nil
		}
	}
	return MemoryResolution{}, fmt.Errorf("memory %d was not found directly or through an exact-dedup alias", requested)
}

func resolveMemoryIn(ctx context.Context, db *sql.DB, database string,
	requested int64) (MemoryResolution, bool, error) {
	read := func(id int64, alias bool) (MemoryResolution, bool, error) {
		result := MemoryResolution{RequestedID: requested, CanonicalID: id, Alias: alias, Database: database}
		err := db.QueryRowContext(ctx, `SELECT layer, content FROM memories WHERE id = ?`, id).
			Scan(&result.Layer, &result.Content)
		switch {
		case err == nil:
			return result, true, nil
		case errors.Is(err, sql.ErrNoRows):
			return MemoryResolution{}, false, nil
		case missingTable(err):
			return MemoryResolution{}, false, nil
		default:
			return MemoryResolution{}, false, err
		}
	}
	if result, found, err := read(requested, false); found || err != nil {
		return result, found, err
	}
	var canonical int64
	err := db.QueryRowContext(ctx, `SELECT canonical_id FROM memory_id_remaps WHERE old_id = ?`, requested).
		Scan(&canonical)
	if errors.Is(err, sql.ErrNoRows) || missingTable(err) {
		return MemoryResolution{}, false, nil
	}
	if err != nil {
		return MemoryResolution{}, false, fmt.Errorf("resolve memory alias %d in %s: %w", requested, database, err)
	}
	result, found, err := read(canonical, true)
	if err != nil {
		return MemoryResolution{}, false, err
	}
	if !found {
		return MemoryResolution{}, false, fmt.Errorf("memory alias %d points to missing canonical id %d", requested, canonical)
	}
	return result, true, nil
}

func missingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}
