package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
)

// CoverageCategory collapses files or records onto the reason they did not
// become corpus. Counts stay exact; paths live in Details for verbose output.
type CoverageCategory struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type CoverageDetail struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type FileCoverage struct {
	Seen     int                `json:"seen"`
	Claimed  int                `json:"claimed"`
	Ingested int                `json:"ingested"`
	Skipped  int                `json:"skipped"`
	Skips    []CoverageCategory `json:"skip_reasons"`
}

type RecordCoverage struct {
	Excluded []CoverageCategory `json:"excluded"`
}

// OpenCodeCoverage compares the live normalized store with the corpus view. It
// deliberately reports only counts: extraction semantics belong to the reader,
// while this report makes their coverage observable.
type OpenCodeCoverage struct {
	Store     map[string]int `json:"store"`
	Extracted map[string]int `json:"extracted"`
}

type CoverageReport struct {
	Files    FileCoverage       `json:"files"`
	Records  RecordCoverage     `json:"records"`
	Gaps     []CoverageCategory `json:"gaps,omitempty"`
	Details  []CoverageDetail   `json:"details,omitempty"`
	OpenCode OpenCodeCoverage   `json:"opencode"`

	skipIndex   map[string]int `json:"-"`
	recordIndex map[string]int `json:"-"`
	gapIndex    map[string]int `json:"-"`
}

func newCoverage(plan Plan) CoverageReport {
	report := CoverageReport{
		Files: FileCoverage{Seen: len(plan.Targets) + len(plan.Excluded), Claimed: len(plan.Targets),
			Skips: []CoverageCategory{}},
		Records:   RecordCoverage{Excluded: []CoverageCategory{}},
		Gaps:      []CoverageCategory{},
		Details:   []CoverageDetail{},
		OpenCode:  OpenCodeCoverage{Store: map[string]int{}, Extracted: map[string]int{}},
		skipIndex: map[string]int{}, recordIndex: map[string]int{}, gapIndex: map[string]int{},
	}
	for _, target := range plan.Excluded {
		report.skip(target.Path, target.ExclusionReason)
		records := excludedRecordCount(target)
		if records > 0 {
			report.addCategory(&report.Records.Excluded, report.recordIndex,
				target.ExclusionReason, records)
		}
	}
	return report
}

func (r *CoverageReport) skip(path, reason string) {
	r.Files.Skipped++
	r.addCategory(&r.Files.Skips, r.skipIndex, reason, 1)
	r.detail(path, reason)
}

func (r *CoverageReport) gap(path, reason string) {
	r.addCategory(&r.Gaps, r.gapIndex, reason, 1)
	r.detail(path, reason)
}

func (r *CoverageReport) detail(path, reason string) {
	if path == "" || len(r.Details) >= discardDetailBudget {
		return
	}
	r.Details = append(r.Details, CoverageDetail{Path: path, Reason: reason})
}

func (*CoverageReport) addCategory(categories *[]CoverageCategory, index map[string]int,
	reason string, count int) {
	if at, ok := index[reason]; ok {
		(*categories)[at].Count += count
		return
	}
	index[reason] = len(*categories)
	*categories = append(*categories, CoverageCategory{Reason: reason, Count: count})
}

func finalizeCoverage(ctx context.Context, db *sql.DB, roots Roots, plan Plan,
	result *Result) {
	for _, link := range plan.ManifestLinks {
		if !link.Exists {
			result.Coverage.gap(link.Path, "Claude memory manifest link is absent from disk")
		}
	}
	if !result.DryRun && len(plan.ManifestLinks) > 0 {
		landed, err := corpusMemoryPaths(ctx, db)
		if err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("memory manifest coverage could not read corpus paths: %v", err))
		} else {
			for _, link := range plan.ManifestLinks {
				if link.Exists && !landed[link.Path] {
					result.Coverage.gap(link.Path, "Claude memory manifest link is absent from corpus")
				}
			}
		}
	}
	if roots.OpenCodeDB == "" || !isFile(roots.OpenCodeDB) {
		return
	}
	store, err := openCodeStoreCoverage(ctx, roots.OpenCodeDB)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("OpenCode coverage could not read its store: %v", err))
		return
	}
	result.Coverage.OpenCode.Store = store
	extracted, err := openCodeExtractedCoverage(ctx, db)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("OpenCode coverage could not read corpus rows: %v", err))
		return
	}
	result.Coverage.OpenCode.Extracted = extracted
}

func corpusMemoryPaths(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT json_extract(metadata, '$.file_path')
		FROM memories WHERE json_extract(metadata, '$.file_path') IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := map[string]bool{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths[path] = true
	}
	return paths, rows.Err()
}

func openCodeStoreCoverage(ctx context.Context, path string) (map[string]int, error) {
	db, err := openForeign(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	counts := map[string]int{}
	for table, label := range map[string]string{
		"session": "sessions", "message": "messages", "part": "parts",
		"todo": "todos", "lost_and_found": "lost_and_found",
	} {
		columns, err := tableColumns(ctx, db, table)
		if err != nil {
			return nil, err
		}
		if len(columns) == 0 {
			continue
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return nil, err
		}
		counts[label] = count
	}
	return counts, nil
}

func openCodeExtractedCoverage(ctx context.Context, db *sql.DB) (map[string]int, error) {
	queries := map[string]string{
		"sessions": `SELECT COUNT(*) FROM sessions WHERE session_id LIKE 'opencode:%'`,
		"exchanges": `SELECT COUNT(*) FROM exchanges e JOIN sessions s ON s.session_id = e.session_id
			WHERE s.session_id LIKE 'opencode:%'`,
		"thinking_blocks": `SELECT COUNT(*) FROM thinking_blocks t JOIN sessions s ON s.session_id = t.session_id
			WHERE s.session_id LIKE 'opencode:%'`,
		"tool_uses": `SELECT COUNT(*) FROM tool_uses t JOIN sessions s ON s.session_id = t.session_id
			WHERE s.session_id LIKE 'opencode:%'`,
	}
	counts := map[string]int{}
	labels := mapsKeys(queries)
	slices.Sort(labels)
	for _, label := range labels {
		var count int
		if err := db.QueryRowContext(ctx, queries[label]).Scan(&count); err != nil {
			return nil, err
		}
		counts[label] = count
	}
	return counts, nil
}

func mapsKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
