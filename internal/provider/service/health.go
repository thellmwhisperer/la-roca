package service

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Health verdicts, in ascending severity.
const (
	HealthPass = "pass"
	HealthWarn = "warn"
	HealthFail = "fail"
)

// defaultHealthRows caps the sample every check returns. A report over a
// database with a million broken rows has to stay a report.
const defaultHealthRows = 10

// HealthRequest is the diagnosis' only knob.
type HealthRequest struct {
	// MaxRows is the sample cap per check. Zero means the default.
	MaxRows int
}

// HealthCheck is one check with its verdict, how many rows it found and a
// sample of them. The count is the truth; the sample is what makes it
// actionable.
type HealthCheck struct {
	Status  string           `json:"status"`
	Count   int              `json:"count"`
	Summary string           `json:"summary"`
	Rows    []map[string]any `json:"rows,omitempty"`
	Extra   map[string]int   `json:"formats,omitempty"`
}

// HealthVerdict is a check name and its status only. Support snapshots use
// this so they never carry finding rows that could hold personal content.
type HealthVerdict struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

const healthSkipped = "skipped"

// HealthReport is the whole diagnosis. Its status is the worst of its checks'.
type HealthReport struct {
	Status      string                 `json:"status"`
	GeneratedAt string                 `json:"generated_at"`
	Checks      map[string]HealthCheck `json:"checks"`
	Version     string                 `json:"version"`
	SourceSHA   string                 `json:"source_sha"`
}

// healthCheck declares one check as data: the count, the sample and how a
// non-zero count is judged. Adding a check is a row, not a fifth copy of the
// same three-step dance.
type healthCheck struct {
	name          string
	summary       string
	severity      string
	memoryOwned   bool
	registryOwned bool
	count         string
	sample        string
}

// The v1 checks. There is deliberately no check over `runs`: that table is v2
// and this binary creates none, and a diagnosis that named it would be naming a
// component this version does not have.
var healthChecks = []healthCheck{
	{
		name:        "orphan_supersedes",
		summary:     "Memories whose supersedes pointer references a memory that is not there.",
		severity:    HealthFail,
		memoryOwned: true,
		count: `SELECT COUNT(*) FROM memories
		        WHERE supersedes IS NOT NULL
		          AND supersedes NOT IN (SELECT id FROM memories)`,
		sample: `SELECT id, supersedes FROM memories
		         WHERE supersedes IS NOT NULL
		           AND supersedes NOT IN (SELECT id FROM memories)
		         ORDER BY id LIMIT ?`,
	},
	{
		name:        "test_metadata_rows",
		summary:     "Live memories carrying the metadata a test leaves behind.",
		severity:    HealthFail,
		memoryOwned: true,
		count: `SELECT COUNT(*) FROM memories
		        WHERE json_valid(metadata)
		          AND json_extract(metadata, '$._test') IN (1, 'true', 'True')`,
		sample: `SELECT id, layer, source_agent FROM memories
		         WHERE json_valid(metadata)
		           AND json_extract(metadata, '$._test') IN (1, 'true', 'True')
		         ORDER BY id LIMIT ?`,
	},
	{
		name:        "test_source_agent_rows",
		summary:     "Live memories written by a test agent.",
		severity:    HealthFail,
		memoryOwned: true,
		count: `SELECT COUNT(*) FROM memories
		        WHERE source_agent IN ('test-agent', 'test')`,
		sample: `SELECT source_agent, COUNT(*) AS count FROM memories
		         WHERE source_agent IN ('test-agent', 'test')
		         GROUP BY source_agent ORDER BY source_agent LIMIT ?`,
	},
	{
		name:          "runtime_layers_not_in_registry",
		summary:       "Layers present in the data and absent from the layer registry.",
		severity:      HealthFail,
		memoryOwned:   true,
		registryOwned: true,
		count: `SELECT COUNT(*) FROM (
		          SELECT m.layer FROM memories m
		          LEFT JOIN layers l ON l.name = m.layer
		          WHERE l.name IS NULL GROUP BY m.layer)`,
		sample: `SELECT m.layer, COUNT(*) AS count FROM memories m
		         LEFT JOIN layers l ON l.name = m.layer
		         WHERE l.name IS NULL
		         GROUP BY m.layer ORDER BY count DESC, m.layer ASC LIMIT ?`,
	},
	{
		name:          "physical_alias_layer_rows",
		summary:       "Memories stored under an alias layer instead of the physical one.",
		severity:      HealthFail,
		memoryOwned:   true,
		registryOwned: true,
		count: `SELECT COUNT(*) FROM memories m
		        JOIN layers l ON l.name = m.layer
		        WHERE l.alias_of IS NOT NULL`,
		sample: `SELECT m.layer, l.alias_of, COUNT(*) AS count FROM memories m
		         JOIN layers l ON l.name = m.layer
		         WHERE l.alias_of IS NOT NULL
		         GROUP BY m.layer, l.alias_of
		         ORDER BY count DESC, m.layer ASC LIMIT ?`,
	},
	{
		name:     "ghost_sessions",
		summary:  "Sessions with activity and no start or end timestamp.",
		severity: HealthWarn,
		count: `SELECT COUNT(*) FROM sessions
		        WHERE started_at IS NULL AND ended_at IS NULL
		          AND (EXISTS (SELECT 1 FROM exchanges e WHERE e.session_id = sessions.session_id)
		            OR EXISTS (SELECT 1 FROM tool_uses t WHERE t.session_id = sessions.session_id))`,
		sample: `SELECT session_id, source_agent, project FROM sessions
		         WHERE started_at IS NULL AND ended_at IS NULL
		           AND (EXISTS (SELECT 1 FROM exchanges e WHERE e.session_id = sessions.session_id)
		             OR EXISTS (SELECT 1 FROM tool_uses t WHERE t.session_id = sessions.session_id))
		         ORDER BY session_id LIMIT ?`,
	},
}

// Health runs the non-destructive checks over live data. It writes nothing, so
// it answers the same in a read-only installation, which is exactly where an
// operator who suspects something reaches for it.
func (s *Service) Health(ctx context.Context, req HealthRequest) (HealthReport, error) {
	if _, err := s.ensureSchema(ctx); err != nil {
		return HealthReport{}, err
	}
	maxRows := req.MaxRows
	if maxRows <= 0 {
		maxRows = defaultHealthRows
	}
	reader, err := s.db.ReadOnly()
	if err != nil {
		return HealthReport{}, err
	}
	memoryReader, closeMemoryReader, err := s.memoryReader(ctx)
	if err != nil {
		return HealthReport{}, err
	}
	defer closeMemoryReader()
	registered, err := s.registeredLayers(ctx)
	if err != nil {
		return HealthReport{}, err
	}

	report := HealthReport{
		Status:      HealthPass,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Checks:      make(map[string]HealthCheck, len(healthChecks)+1),
		Version:     s.opts.Version,
		SourceSHA:   s.opts.Commit,
	}
	for _, check := range healthChecks {
		checkReader := reader
		if check.memoryOwned {
			checkReader = memoryReader
		}
		outcome, err := runHealthCheck(ctx, checkReader, check, registered, maxRows)
		if err != nil {
			return HealthReport{}, err
		}
		report.Checks[check.name] = outcome
		report.Status = worst(report.Status, outcome.Status)
	}

	timestamps, err := checkTimestampFormats(ctx, memoryReader)
	if err != nil {
		return HealthReport{}, err
	}
	report.Checks["memory_created_at_formats"] = timestamps
	report.Status = worst(report.Status, timestamps.Status)
	return report, nil
}

// HealthVerdicts runs the same checks as Health but returns only names and
// statuses. A check is run against every applicable store and keeps the worst
// concrete verdict. Missing handles and tables are skipped.
func HealthVerdicts(ctx context.Context, memories, others []*sql.DB) []HealthVerdict {
	verdicts := make([]HealthVerdict, 0, len(healthChecks)+1)
	for _, check := range healthChecks {
		readers := others
		if check.memoryOwned {
			readers = memories
		}
		status := healthSkipped
		for _, reader := range readers {
			if reader == nil {
				continue
			}
			count, err := healthCount(ctx, reader, check, layersFrom(ctx, reader))
			if err != nil {
				continue
			}
			candidate := HealthPass
			if count > 0 {
				candidate = check.severity
			}
			status = worstConcrete(status, candidate)
		}
		verdicts = append(verdicts, HealthVerdict{Name: check.name, Status: status})
	}
	status := healthSkipped
	for _, reader := range memories {
		if reader == nil {
			continue
		}
		timestamps, err := checkTimestampFormats(ctx, reader)
		if err == nil {
			status = worstConcrete(status, timestamps.Status)
		}
	}
	return append(verdicts, HealthVerdict{Name: "memory_created_at_formats", Status: status})
}

func worstConcrete(current, candidate string) string {
	if current == healthSkipped {
		return candidate
	}
	return worst(current, candidate)
}

func layersFrom(ctx context.Context, db *sql.DB) []registeredLayer {
	if db == nil {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT name, COALESCE(alias_of, '') FROM layers ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var registered []registeredLayer
	for rows.Next() {
		var layer registeredLayer
		if rows.Scan(&layer.name, &layer.aliasOf) != nil {
			return nil
		}
		registered = append(registered, layer)
	}
	if rows.Err() != nil {
		return nil
	}
	return registered
}

func healthQuery(check healthCheck, registered []registeredLayer) (string, []any) {
	if check.registryOwned {
		return layerRegistryCTE(registered)
	}
	return "", nil
}

func healthCount(ctx context.Context, reader *sql.DB, check healthCheck,
	registered []registeredLayer) (int, error) {
	prefix, arguments := healthQuery(check, registered)
	var count int
	if err := reader.QueryRowContext(ctx, prefix+check.count, arguments...).Scan(&count); err != nil {
		return 0, fmt.Errorf("health check %s: %w", check.name, err)
	}
	return count, nil
}

func runHealthCheck(ctx context.Context, reader *sql.DB, check healthCheck,
	registered []registeredLayer, maxRows int) (HealthCheck, error) {
	count, err := healthCount(ctx, reader, check, registered)
	if err != nil {
		return HealthCheck{}, err
	}
	outcome := HealthCheck{Status: HealthPass, Count: count, Summary: check.summary}
	if count == 0 {
		return outcome, nil
	}
	outcome.Status = check.severity
	prefix, arguments := healthQuery(check, registered)
	rows, err := reader.QueryContext(ctx, prefix+check.sample,
		append(slices.Clone(arguments), maxRows)...)
	if err != nil {
		return HealthCheck{}, fmt.Errorf("health check %s: %w", check.name, err)
	}
	defer rows.Close()
	_, outcome.Rows, err = scanRows(rows, 0, "")
	if err != nil {
		return HealthCheck{}, fmt.Errorf("health check %s: %w", check.name, err)
	}
	return outcome, nil
}

func layerRegistryCTE(registered []registeredLayer) (string, []any) {
	if len(registered) == 0 {
		return `WITH layers(name, alias_of) AS (
			SELECT CAST(NULL AS TEXT), CAST(NULL AS TEXT) WHERE 0) `, nil
	}
	values := make([]string, 0, len(registered))
	arguments := make([]any, 0, len(registered)*2)
	for _, layer := range registered {
		values = append(values, "(?, ?)")
		arguments = append(arguments, layer.name, orNull(layer.aliasOf))
	}
	return "WITH layers(name, alias_of) AS (VALUES " + strings.Join(values, ", ") + ") ", arguments
}

var (
	isoOffset  = regexp.MustCompile(`T.*(?:Z|[+-]\d\d:\d\d)$`)
	sqliteBare = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`)
)

// checkTimestampFormats is the one check that is a distribution and not a
// count: what it reports is that the same column is written in two different
// spellings, which is what makes an ORDER BY over it lie.
func checkTimestampFormats(ctx context.Context, reader *sql.DB) (HealthCheck, error) {
	rows, err := reader.QueryContext(ctx,
		"SELECT created_at FROM memories WHERE created_at IS NOT NULL")
	if err != nil {
		return HealthCheck{}, fmt.Errorf("health check memory_created_at_formats: %w", err)
	}
	defer rows.Close()

	buckets := map[string]int{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return HealthCheck{}, err
		}
		switch {
		case isoOffset.MatchString(value):
			buckets["iso_offset"]++
		case sqliteBare.MatchString(value):
			buckets["sqlite_bare"]++
		default:
			buckets["date_only_or_other"]++
		}
	}
	if err := rows.Err(); err != nil {
		return HealthCheck{}, err
	}

	total := 0
	for _, count := range buckets {
		total += count
	}
	outcome := HealthCheck{
		Status:  HealthPass,
		Count:   total,
		Summary: "Distribution of the spellings of memories.created_at.",
		Extra:   buckets,
	}
	if len(buckets) > 1 || buckets["date_only_or_other"] > 0 {
		outcome.Status = HealthWarn
	}
	return outcome, nil
}

// worst is the report's verdict: the most severe of its checks', by the order
// the verdicts are declared in.
func worst(current, candidate string) string {
	return severities[max(slices.Index(severities, current), slices.Index(severities, candidate))]
}

var severities = []string{HealthPass, HealthWarn, HealthFail}
