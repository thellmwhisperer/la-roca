package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/jsonid"
)

// MemoryRecord is one operational memory returned with its full content.
type MemoryRecord struct {
	ID        int64  `json:"id,string"`
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
	Unslugged jsonid.Ints    `json:"unslugged,omitempty"`
}

// PillDeleteResult reports the one destructive operation La Roca exposes:
// retiring every stored version of a pill slug.
type PillDeleteResult struct {
	Slug    string   `json:"slug"`
	Deleted int64    `json:"deleted"`
	Known   []string `json:"known,omitempty"`
}

// HandoffList is the active, unsuperseded handoffs for a project.
type HandoffList struct {
	Project        string         `json:"project"`
	GlobalFallback bool           `json:"global_fallback,omitempty"`
	Handoffs       []MemoryRecord `json:"handoffs"`
}

// HandoffLabRow is the newest current handoff for one project.
type HandoffLabRow struct {
	Project     string `json:"project"`
	LastHandoff string `json:"last_handoff"`
	Head        string `json:"head"`
}

// HandoffLab is the cross-project handoff roster for operator seats.
type HandoffLab struct {
	Since string          `json:"since,omitempty"`
	Rows  []HandoffLabRow `json:"rows"`
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

// DeletePill removes every ops-store version of a pill slug. It is intentionally
// not project-scoped: a pill slug names the artifact being retired.
func (s *Service) DeletePill(ctx context.Context, slug string) (PillDeleteResult, error) {
	if s.opts.ReadOnly {
		return PillDeleteResult{}, refuseReadOnly("delete pill")
	}
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return PillDeleteResult{}, fmt.Errorf("a pill slug is required")
	}
	if !s.opts.RocaOpsEnabled {
		return PillDeleteResult{}, fmt.Errorf("pill delete requires features.roca_ops and the %s database", rocaOpsPluginName)
	}
	if _, err := s.ensureSchema(ctx); err != nil {
		return PillDeleteResult{}, err
	}
	target, err := s.memoryOwner()
	if err != nil {
		return PillDeleteResult{}, err
	}
	result := PillDeleteResult{Slug: slug}
	err = target.Write(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT id, IFNULL(metadata, '{}') FROM memories WHERE layer = 'pill'`)
		if err != nil {
			return fmt.Errorf("list pill slugs: %w", err)
		}
		defer rs.Close()
		var ids []int64
		for rs.Next() {
			var id int64
			var metadata string
			if err := rs.Scan(&id, &metadata); err != nil {
				return fmt.Errorf("read a pill slug: %w", err)
			}
			storedSlug := pillSlug(metadata)
			if storedSlug != "" {
				result.Known = append(result.Known, storedSlug)
			}
			if storedSlug == slug {
				ids = append(ids, id)
			}
		}
		if err := rs.Err(); err != nil {
			return err
		}
		if err := rs.Close(); err != nil {
			return err
		}
		sort.Strings(result.Known)
		result.Known = slices.Compact(result.Known)
		if len(ids) == 0 {
			return fmt.Errorf("no pill with slug %q", slug)
		}
		for _, id := range ids {
			outcome, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE layer = 'pill' AND id = ?`, id)
			if err != nil {
				return fmt.Errorf("delete pill %q: %w", slug, err)
			}
			deleted, err := outcome.RowsAffected()
			if err != nil {
				return err
			}
			result.Deleted += deleted
		}
		return nil
	})
	if err != nil {
		result.Deleted = 0
	}
	return result, err
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

// LatestHandoffsByProject loads the newest active, unsuperseded handoff for
// every project. Global handoffs are intentionally excluded: this view answers
// "which project has what current handoff?" for floating operator seats.
func (s *Service) LatestHandoffsByProject(ctx context.Context, since time.Time, headChars int) (HandoffLab, error) {
	rows, err := s.loadCurrentProjectHandoffs(ctx)
	if err != nil {
		return HandoffLab{}, err
	}
	result := HandoffLab{}
	if !since.IsZero() {
		result.Since = since.UTC().Format(time.RFC3339)
	}
	selected := map[string]loadedMemory{}
	for _, row := range rows {
		if row.Project == "" {
			continue
		}
		if !since.IsZero() {
			if !row.createdAtValid || row.createdAt.Before(since) {
				continue
			}
		}
		previous, seen := selected[row.Project]
		if !seen || compareCreatedAt(row, previous) > 0 ||
			(compareCreatedAt(row, previous) == 0 && row.ID > previous.ID) {
			selected[row.Project] = row
		}
	}
	projects := make([]string, 0, len(selected))
	for project := range selected {
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool {
		left, right := selected[projects[i]], selected[projects[j]]
		if compared := compareCreatedAt(left, right); compared != 0 {
			return compared > 0
		}
		return projects[i] < projects[j]
	})
	for _, project := range projects {
		row := selected[project]
		result.Rows = append(result.Rows, HandoffLabRow{
			Project:     project,
			LastHandoff: row.CreatedAt,
			Head:        TextHead(row.Content, headChars),
		})
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
	return s.loadCurrentHandoffsWhere(ctx, "load current handoff memories",
		"AND ((? <> '' AND candidate.project = ?) OR candidate.project IS NULL)", project, project)
}

func (s *Service) loadCurrentProjectHandoffs(ctx context.Context) ([]loadedMemory, error) {
	return s.loadCurrentHandoffsWhere(ctx, "load current project handoff memories",
		"AND candidate.project IS NOT NULL AND candidate.project <> ''")
}

func (s *Service) loadCurrentHandoffsWhere(ctx context.Context, failure, scopePredicate string,
	args ...any) ([]loadedMemory, error) {
	reader, closeReader, err := s.sessionContextReader(ctx)
	if err != nil {
		return nil, err
	}
	defer closeReader()

	query := `
		SELECT candidate.id, candidate.layer, candidate.content,
		       IFNULL(candidate.metadata, '{}'), IFNULL(candidate.project, ''),
		       candidate.status, IFNULL(candidate.created_at, '')
		FROM memories AS candidate
		WHERE candidate.layer = 'handoff'
		  AND candidate.status = 'active'
		  ` + scopePredicate + `
		  AND NOT EXISTS (
		      SELECT 1 FROM memories AS replacement
		      WHERE replacement.supersedes = candidate.id
		  )
		ORDER BY candidate.created_at DESC, candidate.id DESC`
	rs, err := reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", failure, err)
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

// TextHead returns a rune-bounded prefix for session context previews.
func TextHead(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
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
