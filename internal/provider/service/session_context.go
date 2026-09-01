package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
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
	Metadata       string
	createdAt      time.Time
	createdAtValid bool
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
	newest := map[string]loadedMemory{}
	for _, row := range rows {
		slug := pillSlug(row.Metadata)
		if slug == "" {
			result.Unslugged = append(result.Unslugged, row.ID)
			continue
		}
		row.Slug = slug
		previous, seen := newest[slug]
		if !seen || compareCreatedAt(row, previous) > 0 ||
			(compareCreatedAt(row, previous) == 0 && row.ID > previous.ID) {
			newest[slug] = row
		}
	}
	selected := make([]loadedMemory, 0, len(newest))
	for _, pill := range newest {
		selected = append(selected, pill)
	}
	sort.Slice(selected, func(i, j int) bool {
		if compared := compareCreatedAt(selected[i], selected[j]); compared != 0 {
			return compared > 0
		}
		if selected[i].Slug != selected[j].Slug {
			return selected[i].Slug < selected[j].Slug
		}
		return selected[i].ID > selected[j].ID
	})
	result.Pills = make([]MemoryRecord, 0, len(selected))
	for _, pill := range selected {
		result.Pills = append(result.Pills, pill.MemoryRecord)
	}
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

	query := `SELECT id, layer, content, IFNULL(metadata, '{}'), IFNULL(project, ''), status, IFNULL(created_at, '')
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

	return scanLoadedMemories(rs, layer)
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
		       candidate.status, IFNULL(candidate.created_at, '')
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

	rows, err := scanLoadedMemories(rs, "handoff")
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool {
		if compared := compareCreatedAt(rows[i], rows[j]); compared != 0 {
			return compared > 0
		}
		return rows[i].ID > rows[j].ID
	})
	return rows, nil
}

func scanLoadedMemories(rs *sql.Rows, layer string) ([]loadedMemory, error) {
	var rows []loadedMemory
	for rs.Next() {
		var row loadedMemory
		if err := rs.Scan(&row.ID, &row.Layer, &row.Content, &row.Metadata, &row.Project,
			&row.Status, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("read a %s memory: %w", layer, err)
		}
		row.createdAt, row.createdAtValid = normalizeCreatedAt(row.CreatedAt)
		rows = append(rows, row)
	}
	return rows, rs.Err()
}

func compareCreatedAt(left, right loadedMemory) int {
	if left.createdAtValid != right.createdAtValid {
		if left.createdAtValid {
			return 1
		}
		return -1
	}
	if !left.createdAtValid || left.createdAt.Equal(right.createdAt) {
		return 0
	}
	if left.createdAt.After(right.createdAt) {
		return 1
	}
	return -1
}

func normalizeCreatedAt(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), true
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
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
