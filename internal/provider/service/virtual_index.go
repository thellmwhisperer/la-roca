package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/ingestprovenance"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

// DefaultIndexTokenBudget is the shipped ceiling for the virtual index.
const DefaultIndexTokenBudget = 8000

const (
	virtualIndexCacheFile    = "virtual-index.json"
	virtualIndexCacheVersion = 1
)

// VirtualIndexRequest is the only knob the map accepts.
type VirtualIndexRequest struct {
	TokenBudget int
	GeneratedAt time.Time
	Refresh     bool
}

// VirtualIndex is the derived MEMORY.md: one hook line per knowledge block.
type VirtualIndex struct {
	GeneratedAt string   `json:"generated_at"`
	Budget      int      `json:"budget"`
	Tokens      int      `json:"tokens"`
	Truncated   bool     `json:"truncated"`
	Omitted     int      `json:"omitted,omitempty"`
	Text        string   `json:"text"`
	Blocks      []string `json:"blocks"`
}

type virtualIndexCache struct {
	Version      int          `json:"version"`
	Budget       int          `json:"budget"`
	VirtualIndex VirtualIndex `json:"index"`
}

type sourceVolume struct {
	sessions, exchanges int
	yearFrom, yearTo    string
}

type coverage struct{ rows, indexed int }

// VirtualIndex returns the compact map of what this installation holds.
// Generation is deterministic SQL only. A cache next to the database keeps
// the read instant; ingest refreshes it.
func (s *Service) VirtualIndex(ctx context.Context, req VirtualIndexRequest) (VirtualIndex, error) {
	if _, err := s.ensureSchema(ctx); err != nil {
		return VirtualIndex{}, err
	}
	budget := s.indexTokenBudget(req.TokenBudget)
	generatedAt := req.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	if !req.Refresh {
		if cached, ok := s.readVirtualIndexCache(budget); ok {
			return cached, nil
		}
	}
	report, err := s.generateVirtualIndex(ctx, budget, generatedAt)
	if err != nil {
		return VirtualIndex{}, err
	}
	if !s.opts.ReadOnly {
		if err := s.writeVirtualIndexCache(report); err != nil {
			return VirtualIndex{}, err
		}
	}
	return report, nil
}

func (s *Service) refreshVirtualIndex(ctx context.Context) error {
	_, err := s.VirtualIndex(ctx, VirtualIndexRequest{Refresh: true})
	return err
}

func (s *Service) indexTokenBudget(requested int) int {
	if requested > 0 {
		return requested
	}
	if s.opts.IndexTokenBudget > 0 {
		return s.opts.IndexTokenBudget
	}
	return DefaultIndexTokenBudget
}

func (s *Service) virtualIndexCachePath() string {
	dir := s.dataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, virtualIndexCacheFile)
}

func (s *Service) readVirtualIndexCache(budget int) (VirtualIndex, bool) {
	path := s.virtualIndexCachePath()
	if path == "" {
		return VirtualIndex{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return VirtualIndex{}, false
	}
	var cached virtualIndexCache
	if json.Unmarshal(raw, &cached) != nil || cached.Version != virtualIndexCacheVersion ||
		cached.Budget != budget || cached.VirtualIndex.Text == "" ||
		cached.VirtualIndex.Tokens != countIndexTokens(cached.VirtualIndex.Text) ||
		cached.VirtualIndex.Tokens > budget {
		return VirtualIndex{}, false
	}
	return cached.VirtualIndex, true
}

func (s *Service) writeVirtualIndexCache(report VirtualIndex) error {
	path := s.virtualIndexCachePath()
	if path == "" {
		return nil
	}
	raw, err := json.Marshal(virtualIndexCache{
		Version: virtualIndexCacheVersion, Budget: report.Budget, VirtualIndex: report,
	})
	if err != nil {
		return fmt.Errorf("encode the virtual index cache: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write the virtual index cache: %w", err)
	}
	return nil
}

func (s *Service) generateVirtualIndex(ctx context.Context, budget int, generatedAt time.Time) (VirtualIndex, error) {
	blocks, err := s.virtualIndexBlocks(ctx)
	if err != nil {
		return VirtualIndex{}, err
	}
	stamp := generatedAt.Format(time.RFC3339)
	text, tokens, omitted, err := fitVirtualIndex(stamp, budget, blocks)
	if err != nil {
		return VirtualIndex{}, err
	}
	return VirtualIndex{
		GeneratedAt: stamp,
		Budget:      budget,
		Tokens:      tokens,
		Truncated:   omitted > 0,
		Omitted:     omitted,
		Text:        text,
		Blocks:      blocks[:len(blocks)-omitted],
	}, nil
}

func (s *Service) virtualIndexBlocks(ctx context.Context) ([]string, error) {
	corpus, err := s.indexCorpusReaders()
	if err != nil {
		return nil, err
	}
	memory, closeMemory, err := s.memoryReader(ctx)
	if err != nil {
		return nil, err
	}
	defer closeMemory()

	var blocks []string
	sources, err := collectSourceVolumes(ctx, corpus.readers)
	if err != nil {
		return nil, err
	}
	for _, agent := range sortedKeys(sources) {
		blocks = append(blocks, formatSourceBlock(agent, sources[agent]))
	}

	layers, err := collectMemoryLayers(ctx, memory)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, layers...)

	families, err := collectCorpusFamilies(ctx, corpus.readers)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, families...)

	fts, err := collectFTSCoverage(ctx, corpus.readers, memory)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, fts...)
	blocks = append(blocks, s.vectorIndexBlock())

	health, err := s.Health(ctx, HealthRequest{MaxRows: 1})
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, formatHealthBlock(health))

	gaps, err := collectGaps(ctx, corpus.readers, sources)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, gaps...)
	return blocks, nil
}

type indexReaders struct {
	readers []*sql.DB
}

func (s *Service) indexCorpusReaders() (indexReaders, error) {
	var out indexReaders
	add := func(db *store.DB) error {
		if db == nil {
			return nil
		}
		reader, err := db.ReadOnly()
		if err != nil {
			return err
		}
		out.readers = append(out.readers, reader)
		return nil
	}
	if s.servingLayout() != LayoutLegacyServing {
		return out, add(s.db)
	}
	if err := add(s.db); err != nil {
		return out, err
	}
	if s.corpus != nil && s.corpus != s.db {
		if err := add(s.corpus); err != nil {
			return out, err
		}
	}
	return out, nil
}

func collectSourceVolumes(ctx context.Context, readers []*sql.DB) (map[string]sourceVolume, error) {
	volumes := map[string]sourceVolume{}
	for _, reader := range readers {
		rows, err := reader.QueryContext(ctx, `
			SELECT COALESCE(NULLIF(s.source_agent, ''), 'unknown') AS source_agent,
			       COUNT(DISTINCT s.session_id) AS sessions,
			       COUNT(e.id) AS exchanges,
			       MIN(CASE
			         WHEN length(s.started_at) >= 4 AND length(s.ended_at) >= 4
			           THEN min(substr(s.started_at, 1, 4), substr(s.ended_at, 1, 4))
			         WHEN length(s.started_at) >= 4 THEN substr(s.started_at, 1, 4)
			         WHEN length(s.ended_at) >= 4 THEN substr(s.ended_at, 1, 4)
			       END),
			       MAX(CASE
			         WHEN length(s.started_at) >= 4 AND length(s.ended_at) >= 4
			           THEN max(substr(s.started_at, 1, 4), substr(s.ended_at, 1, 4))
			         WHEN length(s.started_at) >= 4 THEN substr(s.started_at, 1, 4)
			         WHEN length(s.ended_at) >= 4 THEN substr(s.ended_at, 1, 4)
			       END)
			FROM sessions s
			LEFT JOIN exchanges e ON e.session_id = s.session_id
			GROUP BY 1
			ORDER BY 1`)
		if err != nil {
			return nil, fmt.Errorf("index source volumes: %w", err)
		}
		for rows.Next() {
			var agent string
			var volume sourceVolume
			var yearFrom, yearTo sql.NullString
			if err := rows.Scan(&agent, &volume.sessions, &volume.exchanges, &yearFrom, &yearTo); err != nil {
				rows.Close()
				return nil, err
			}
			volume.yearFrom, volume.yearTo = yearFrom.String, yearTo.String
			volumes[agent] = mergeSourceVolume(volumes[agent], volume)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return volumes, nil
}

func mergeSourceVolume(current, extra sourceVolume) sourceVolume {
	merged := sourceVolume{
		sessions:  current.sessions + extra.sessions,
		exchanges: current.exchanges + extra.exchanges,
		yearFrom:  minYear(current.yearFrom, extra.yearFrom),
		yearTo:    maxYear(current.yearTo, extra.yearTo),
	}
	return merged
}

func formatSourceBlock(agent string, volume sourceVolume) string {
	line := "source " + agent
	if span := yearSpan(volume.yearFrom, volume.yearTo); span != "" {
		line += " " + span
	}
	line += fmt.Sprintf(": %d ses / %d exch", volume.sessions, volume.exchanges)
	if tail := epochTail(agent); tail != "" {
		line += " — " + tail
	}
	return line
}

func epochTail(source string) string {
	switch source {
	case "chatgpt-web", "claude-web":
		return "pre-agentic"
	case "unknown":
		return ""
	default:
		if ingestprovenance.HarnessForSource(source) != "" {
			return "agentic"
		}
		return ""
	}
}

func collectMemoryLayers(ctx context.Context, reader *sql.DB) ([]string, error) {
	rows, err := reader.QueryContext(ctx, `
		SELECT layer, origin, COUNT(*),
		       MAX(CASE WHEN length(created_at) >= 10 THEN substr(created_at, 1, 10) END)
		FROM memories
		GROUP BY layer, origin
		ORDER BY layer, origin`)
	if err != nil {
		return nil, fmt.Errorf("index memory layers: %w", err)
	}
	defer rows.Close()
	var blocks []string
	for rows.Next() {
		var layer, origin string
		var count int
		var last sql.NullString
		if err := rows.Scan(&layer, &origin, &count, &last); err != nil {
			return nil, err
		}
		line := fmt.Sprintf("memory %s/%s: %d", layer, origin, count)
		if last.String != "" {
			line += " — last " + last.String
		}
		blocks = append(blocks, line)
	}
	return blocks, rows.Err()
}

func collectCorpusFamilies(ctx context.Context, readers []*sql.DB) ([]string, error) {
	totals := map[string]int{}
	for _, family := range corpusFamilies {
		for _, reader := range readers {
			count, _, err := countTable(ctx, reader, family.table)
			if err != nil {
				return nil, err
			}
			totals[family.name] += count
		}
	}
	blocks := make([]string, 0, len(corpusFamilies))
	for _, family := range corpusFamilies {
		blocks = append(blocks, fmt.Sprintf("corpus %s: %d", family.name, totals[family.name]))
	}
	return blocks, nil
}

var corpusFamilies = []struct{ name, table string }{
	{"sessions", "sessions"},
	{"exchanges", "exchanges"},
	{"thinking", "thinking_blocks"},
	{"tool_uses", "tool_uses"},
}

func collectFTSCoverage(ctx context.Context, corpus []*sql.DB, memory *sql.DB) ([]string, error) {
	type family struct {
		name   string
		table  string
		fts    string
		memory bool
	}
	families := []family{
		{name: "memories", table: "memories", fts: "memories_fts", memory: true},
		{name: "exchanges", table: "exchanges", fts: "exchanges_fts"},
		{name: "thinking", table: "thinking_blocks", fts: "thinking_fts"},
		{name: "sessions", table: "sessions", fts: "sessions_fts"},
	}
	var blocks []string
	for _, family := range families {
		var cover coverage
		var err error
		if family.memory {
			cover, err = sumCoverage(ctx, []*sql.DB{memory}, family.table, family.fts)
		} else {
			cover, err = sumCoverage(ctx, corpus, family.table, family.fts)
		}
		if err != nil {
			return nil, err
		}
		if cover.indexed < 0 {
			blocks = append(blocks, "index fts "+family.name+": not built")
			continue
		}
		blocks = append(blocks, fmt.Sprintf("index fts %s: %d/%d", family.name, cover.indexed, cover.rows))
	}
	return blocks, nil
}

func sumCoverage(ctx context.Context, readers []*sql.DB, table, fts string) (coverage, error) {
	var total coverage
	seenFTS := false
	for _, reader := range readers {
		rows, ok, err := countTable(ctx, reader, table)
		if err != nil {
			return coverage{}, err
		}
		if ok {
			total.rows += rows
		}
		indexed, ok, err := countTable(ctx, reader, fts)
		if err != nil {
			return coverage{}, err
		}
		if ok {
			total.indexed += indexed
			seenFTS = true
		}
	}
	if !seenFTS {
		total.indexed = -1
	}
	return total, nil
}

func (s *Service) vectorIndexBlock() string {
	if s.opts.PluginDir == "" {
		return "index vector: not installed"
	}
	state := filepath.Join(s.opts.PluginDir, "vector", "state")
	vectorPath := filepath.Join(state, "vector.db")
	if _, err := os.Stat(vectorPath); err != nil {
		return "index vector: not installed"
	}
	db, err := sql.Open("sqlite", vectorPath)
	if err != nil {
		return "index vector: not installed"
	}
	defer db.Close()
	var model, dims string
	_ = db.QueryRow(`SELECT value FROM meta WHERE key='model'`).Scan(&model)
	_ = db.QueryRow(`SELECT value FROM meta WHERE key='dimensions'`).Scan(&dims)
	var chunks int
	_ = db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&chunks)
	line := "index vector:"
	if model != "" {
		line += " model=" + model
	}
	if dims != "" {
		line += " dims=" + dims
	}
	line += fmt.Sprintf(" chunks=%d", chunks)
	if last := lastVectorDelta(state, db); last != "" {
		line += " last=" + last
	}
	if model == "" && dims == "" && chunks == 0 {
		return "index vector: not installed"
	}
	return strings.TrimSpace(line)
}

func lastVectorDelta(state string, db *sql.DB) string {
	raw, err := os.ReadFile(filepath.Join(state, "completion.json"))
	if err == nil {
		var completion struct {
			FinishedAt time.Time `json:"finished_at"`
		}
		if json.Unmarshal(raw, &completion) == nil && !completion.FinishedAt.IsZero() {
			return completion.FinishedAt.UTC().Format(time.RFC3339)
		}
	}
	var updated sql.NullString
	if db.QueryRow(`SELECT MAX(updated_at) FROM chunks`).Scan(&updated) == nil && updated.String != "" {
		if len(updated.String) >= 10 {
			return updated.String[:10]
		}
		return updated.String
	}
	return ""
}

func formatHealthBlock(report HealthReport) string {
	pass, warn, fail := 0, 0, 0
	for _, check := range report.Checks {
		switch check.Status {
		case HealthFail:
			fail++
		case HealthWarn:
			warn++
		default:
			pass++
		}
	}
	return fmt.Sprintf("health: %d pass / %d warn / %d fail", pass, warn, fail)
}

func collectGaps(ctx context.Context, readers []*sql.DB, sources map[string]sourceVolume) ([]string, error) {
	undated := 0
	empty := map[string]bool{}
	for _, reader := range readers {
		var count int
		if err := reader.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM sessions
			WHERE COALESCE(started_at, '') = '' AND COALESCE(ended_at, '') = ''`).Scan(&count); err != nil {
			return nil, fmt.Errorf("index undated sessions: %w", err)
		}
		undated += count
		rows, err := reader.QueryContext(ctx, `
			SELECT DISTINCT source_agent FROM ingest_file_state
			WHERE COALESCE(source_agent, '') != ''
			ORDER BY source_agent`)
		if err != nil {
			return nil, fmt.Errorf("index empty sources: %w", err)
		}
		for rows.Next() {
			var agent string
			if err := rows.Scan(&agent); err != nil {
				rows.Close()
				return nil, err
			}
			if _, present := sources[agent]; !present {
				empty[agent] = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	blocks := []string{fmt.Sprintf("gap undated sessions: %d", undated)}
	if names := sortedKeys(empty); len(names) > 0 {
		blocks = append(blocks, "gap empty sources: "+strings.Join(names, ", "))
	}
	return blocks, nil
}

func countTable(ctx context.Context, reader *sql.DB, table string) (int, bool, error) {
	var count int
	err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("count %s: %w", table, err)
	}
	return count, true, nil
}

func fitVirtualIndex(generatedAt string, budget int, blocks []string) (string, int, int, error) {
	_, minimum := stableVirtualIndex(generatedAt, budget, 0, nil)
	if minimum > budget {
		return "", 0, 0, fmt.Errorf(
			"index token budget %d cannot fit the mandatory header (%d tokens required)",
			budget, minimum)
	}
	kept := append([]string(nil), blocks...)
	for {
		omitted := len(blocks) - len(kept)
		text, tokens := stableVirtualIndex(generatedAt, budget, omitted, kept)
		if tokens <= budget {
			return text, tokens, omitted, nil
		}
		if len(kept) == 0 {
			return "", 0, 0, fmt.Errorf(
				"index token budget %d cannot fit the explicit truncation header (%d tokens required)",
				budget, tokens)
		}
		kept = kept[:len(kept)-1]
	}
}

func stableVirtualIndex(generatedAt string, budget, omitted int, kept []string) (string, int) {
	used := 0
	for {
		text, tokens := composeVirtualIndex(generatedAt, budget, used, omitted, kept)
		if tokens == used {
			return text, tokens
		}
		used = tokens
	}
}

func composeVirtualIndex(generatedAt string, budget, used, omitted int, kept []string) (string, int) {
	var b strings.Builder
	b.WriteString("# La Roca index\n")
	b.WriteString("generated: " + generatedAt + "\n")
	b.WriteString(fmt.Sprintf("budget: %d/%d tokens\n", used, budget))
	if omitted > 0 {
		b.WriteString(fmt.Sprintf("truncated: %d blocks omitted\n", omitted))
	}
	if len(kept) > 0 {
		b.WriteString("\n")
		b.WriteString(strings.Join(kept, "\n"))
		b.WriteString("\n")
	}
	text := b.String()
	return text, countIndexTokens(text)
}

func countIndexTokens(text string) int {
	return len(text)
}

func yearSpan(from, to string) string {
	if from == "" {
		return ""
	}
	if to == "" || to == from {
		return from
	}
	return from + "-" + to
}

func minYear(left, right string) string {
	switch {
	case left == "":
		return right
	case right == "":
		return left
	case left < right:
		return left
	default:
		return right
	}
}

func maxYear(left, right string) string {
	switch {
	case left == "":
		return right
	case right == "":
		return left
	case left > right:
		return left
	default:
		return right
	}
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
