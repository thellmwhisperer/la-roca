package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// MemoryRecord is one operational memory returned with its full content.
type MemoryRecord struct {
	ID        int64  `json:"id"`
	Layer     string `json:"layer"`
	Slug      string `json:"slug,omitempty"`
	Project   string `json:"project,omitempty"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Content   string `json:"content"`
}

// PillList is the active pill roster for a project, after slug dedupe.
type PillList struct {
	Project   string         `json:"project"`
	Pills     []MemoryRecord `json:"pills"`
	Unslugged []int64        `json:"unslugged,omitempty"`
}

// HandoffList is the active, unsuperseded handoffs for a project.
type HandoffList struct {
	Project        string         `json:"project"`
	GlobalFallback bool           `json:"global_fallback,omitempty"`
	Handoffs       []MemoryRecord `json:"handoffs"`
}

type loadedMemory struct {
	MemoryRecord
	Metadata string
}

// ListPills loads active pills for the project, including globals, then keeps
// one row per metadata.pill_slug: the newest by created_at. Rows without a slug
// are named by id and not loaded as pills.
func (s *Service) ListPills(ctx context.Context, project string) (PillList, error) {
	rows, err := s.loadLayer(ctx, "pill", project, true)
	if err != nil {
		return PillList{}, err
	}
	result := PillList{Project: project}
	newest := map[string]MemoryRecord{}
	for _, row := range rows {
		slug := pillSlug(row.Metadata)
		if slug == "" {
			result.Unslugged = append(result.Unslugged, row.ID)
			continue
		}
		row.Slug = slug
		previous, seen := newest[slug]
		if !seen || row.CreatedAt > previous.CreatedAt ||
			(row.CreatedAt == previous.CreatedAt && row.ID > previous.ID) {
			newest[slug] = row.MemoryRecord
		}
	}
	result.Pills = make([]MemoryRecord, 0, len(newest))
	for _, pill := range newest {
		result.Pills = append(result.Pills, pill)
	}
	sort.Slice(result.Pills, func(i, j int) bool {
		if result.Pills[i].CreatedAt == result.Pills[j].CreatedAt {
			return result.Pills[i].Slug < result.Pills[j].Slug
		}
		return result.Pills[i].CreatedAt > result.Pills[j].CreatedAt
	})
	sort.Slice(result.Unslugged, func(i, j int) bool { return result.Unslugged[i] < result.Unslugged[j] })
	return result, nil
}

// ShowPill returns the one complete pill that ListPills would keep for slug.
func (s *Service) ShowPill(ctx context.Context, project, slug string) (MemoryRecord, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return MemoryRecord{}, fmt.Errorf("a pill slug is required")
	}
	list, err := s.ListPills(ctx, project)
	if err != nil {
		return MemoryRecord{}, err
	}
	for _, pill := range list.Pills {
		if pill.Slug == slug {
			return pill, nil
		}
	}
	return MemoryRecord{}, fmt.Errorf("no active pill with slug %q for project %q", slug, project)
}

// LatestHandoffs loads active handoffs for the project that no other memory has
// superseded. It is not newest-by-clock: a later row that does not name a
// predecessor leaves that predecessor current. When the project has none, it
// falls back to global handoffs (project IS NULL).
func (s *Service) LatestHandoffs(ctx context.Context, project string) (HandoffList, error) {
	rows, err := s.loadCurrentHandoffs(ctx, project)
	if err != nil {
		return HandoffList{}, err
	}
	result := HandoffList{Project: project}
	var globals []MemoryRecord
	for _, row := range rows {
		if project != "" && row.Project == project {
			result.Handoffs = append(result.Handoffs, row.MemoryRecord)
		} else if row.Project == "" {
			globals = append(globals, row.MemoryRecord)
		}
	}
	if len(result.Handoffs) == 0 {
		result.Handoffs = globals
		result.GlobalFallback = project != ""
	}
	return result, nil
}

func (s *Service) sessionContextReader(ctx context.Context) (*sql.DB, func(), error) {
	if !s.opts.RocaOpsEnabled {
		return nil, func() {}, fmt.Errorf("session context requires features.roca_ops and the %s database", rocaOpsPluginName)
	}
	return s.memoryReader(ctx)
}

func (s *Service) loadLayer(ctx context.Context, layer, project string, includeGlobal bool) ([]loadedMemory, error) {
	reader, closeReader, err := s.sessionContextReader(ctx)
	if err != nil {
		return nil, err
	}
	defer closeReader()

	query := `SELECT id, layer, content, IFNULL(metadata, '{}'), IFNULL(project, ''), status, created_at
		FROM memories
		WHERE layer = ? AND status = 'active'`
	args := []any{layer}
	switch {
	case includeGlobal && project != "":
		query += " AND (project = ? OR project IS NULL)"
		args = append(args, project)
	case project != "":
		query += " AND project = ?"
		args = append(args, project)
	default:
		query += " AND project IS NULL"
	}
	query += " ORDER BY created_at DESC, id DESC"

	rs, err := reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load %s memories: %w", layer, err)
	}
	defer rs.Close()

	var rows []loadedMemory
	for rs.Next() {
		var row loadedMemory
		if err := rs.Scan(&row.ID, &row.Layer, &row.Content, &row.Metadata, &row.Project,
			&row.Status, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("read a %s memory: %w", layer, err)
		}
		rows = append(rows, row)
	}
	return rows, rs.Err()
}

func (s *Service) loadCurrentHandoffs(ctx context.Context, project string) ([]loadedMemory, error) {
	reader, closeReader, err := s.sessionContextReader(ctx)
	if err != nil {
		return nil, err
	}
	defer closeReader()

	rs, err := reader.QueryContext(ctx, `
		SELECT candidate.id, candidate.layer, candidate.content,
		       IFNULL(candidate.metadata, '{}'), IFNULL(candidate.project, ''),
		       candidate.status, candidate.created_at
		FROM memories AS candidate
		WHERE candidate.layer = 'handoff'
		  AND candidate.status = 'active'
		  AND ((? <> '' AND candidate.project = ?) OR candidate.project IS NULL)
		  AND NOT EXISTS (
		      SELECT 1 FROM memories AS replacement
		      WHERE replacement.supersedes = candidate.id
		  )
		ORDER BY candidate.created_at DESC, candidate.id DESC`, project, project)
	if err != nil {
		return nil, fmt.Errorf("load current handoff memories: %w", err)
	}
	defer rs.Close()

	var rows []loadedMemory
	for rs.Next() {
		var row loadedMemory
		if err := rs.Scan(&row.ID, &row.Layer, &row.Content, &row.Metadata, &row.Project,
			&row.Status, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("read a handoff memory: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, rs.Err()
}

func pillSlug(metadata string) string {
	if strings.TrimSpace(metadata) == "" {
		return ""
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(metadata), &decoded); err != nil {
		return ""
	}
	value, ok := decoded["pill_slug"]
	if !ok || value == nil {
		return ""
	}
	slug, _ := value.(string)
	return strings.TrimSpace(slug)
}
