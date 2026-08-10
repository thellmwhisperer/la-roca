package service

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/hooks"
)

// The session-lifecycle reads and the one write, job J3: what a fresh session is
// handed, and what is preserved when one ends.
//
// They live in the service and not in the hook because of the law the whole
// product obeys: a hook reaches the kernel by running a command, never by
// opening the database. Anything here is therefore reachable from a shell, from
// a script and from the plug, which is exactly the point.

// handoffLimit is how many handoffs a session receives. Three, the laboratory's
// number: a session handed every handoff it ever had would be handed nothing
// else.
const handoffLimit = 3

// The two moments worth preserving. Both mean the same thing: the context is
// about to be lost.
const (
	TriggerPreCompact  = "precompact"
	TriggerSessionEnd  = "session_end"
	sectionPills       = "pills"
	sectionHandoff     = "handoff"
	titlePills         = "ROCA PILLS (base context served from La Roca):"
	titleHandoff       = "ROCA HANDOFF (where the previous sessions stopped):"
	sourceMemoriesHead = "## Source Memories"
)

// triggers is what each one writes into the handoff, so that reading it back
// says why it exists.
var triggers = map[string]struct{ event, reason string }{
	TriggerPreCompact: {"PreCompact",
		"The context is about to be compacted, so this handoff preserves the continuity."},
	TriggerSessionEnd: {"SessionEnd", "The session ended."},
}

// ContextRequest is one session asking for what it should already know.
type ContextRequest struct {
	// Project scopes the reads. Empty means the memories with no project.
	Project string
	// MaxChars is the injection budget. Zero means the resolved default.
	MaxChars int
	// Roster names the pills to serve. RosterDeclared tells "serve none" apart
	// from "nobody said", which is what lets an operator turn the pills off for
	// one session by declaring the roster empty.
	Roster         []string
	RosterDeclared bool
}

// ContextAnswer is the block and what the budget did to it. The numbers travel
// with the text because the budget is a contract and a caller has to be able to
// assert on it.
type ContextAnswer struct {
	Context string             `json:"context"`
	Budget  hooks.BudgetReport `json:"budget"`
	// Source says who decided the roster: the caller or the data.
	Source string `json:"roster_source"`
}

// HandoffRequest is one lifecycle moment to preserve.
type HandoffRequest struct {
	Trigger string
	Session string
	CWD     string
	Project string
	Agent   string
	Surface string
	// At is the timestamp written into the handoff. Empty means now.
	At string
}

// SessionContext renders everything a session start should receive, under
// budget.
func (s *Service) SessionContext(ctx context.Context, req ContextRequest) (ContextAnswer, error) {
	roster, source, err := s.roster(ctx, req)
	if err != nil {
		return ContextAnswer{}, err
	}
	pills, err := s.pills(ctx, roster, req.Project)
	if err != nil {
		return ContextAnswer{}, err
	}
	handoffs, err := s.handoffs(ctx, req.Project, handoffLimit)
	if err != nil {
		return ContextAnswer{}, err
	}
	return s.render(req, source, []hooks.Section{
		{Name: sectionPills, Title: titlePills, Body: strings.Join(pills, "\n\n")},
		{Name: sectionHandoff, Title: titleHandoff, Body: strings.Join(handoffs, "\n")},
	}), nil
}

// LatestHandoff renders only the most recent handoff, under the same budget. It
// is what a session asks for when it wants to know where the previous one
// stopped and nothing else.
func (s *Service) LatestHandoff(ctx context.Context, req ContextRequest) (ContextAnswer, error) {
	handoffs, err := s.handoffs(ctx, req.Project, 1)
	if err != nil {
		return ContextAnswer{}, err
	}
	return s.render(req, "", []hooks.Section{
		{Name: sectionHandoff, Title: titleHandoff, Body: strings.Join(handoffs, "\n")},
	}), nil
}

// RecordHandoff preserves one lifecycle moment. It is a single call into the
// write primitive, so the validation, the layer normalization and the
// deduplication are the same ones every other writer gets: a hook that fires
// twice over the same session does not write the handoff twice.
func (s *Service) RecordHandoff(ctx context.Context, req HandoffRequest) (StoreResult, error) {
	spec, ok := triggers[req.Trigger]
	if !ok {
		return StoreResult{}, fmt.Errorf(
			"I do not know the trigger %q. The ones that exist are: %s",
			req.Trigger, strings.Join(slices.Sorted(maps.Keys(triggers)), ", "))
	}
	session := valueOr(req.Session, "unknown")
	stamp := valueOr(req.At, time.Now().UTC().Format(time.RFC3339))
	return s.Store(ctx, StoreRequest{
		Layer: "handoff",
		Content: fmt.Sprintf("%s in session %s. Working directory: %s. Timestamp: %s. %s",
			spec.event, session, valueOr(req.CWD, "unknown"), stamp, spec.reason),
		Origin:      "agent",
		SourceAgent: valueOr(req.Agent, "claude-code"),
		Project:     req.Project,
		Surface:     valueOr(req.Surface, SurfaceCLI),
		Metadata:    map[string]any{"session_id": session, "trigger": req.Trigger},
	})
}

func (s *Service) render(req ContextRequest, source string,
	sections []hooks.Section) ContextAnswer {
	limit := req.MaxChars
	if limit == 0 {
		limit = hooks.ResolveLimit("", "")
	}
	text, report := hooks.Render(sections, limit)
	return ContextAnswer{Context: text, Budget: report, Source: source}
}

// roster resolves which pills this session receives: what the caller declared,
// or what La Roca serves. The content is served, never compiled: a pill joins
// the roster by existing and leaves it by saying `session_start: false` in its
// own metadata.
func (s *Service) roster(ctx context.Context, req ContextRequest) ([]string, string, error) {
	if req.RosterDeclared {
		return req.Roster, "declared", nil
	}
	rows, err := s.readPills(ctx, req.Project)
	if err != nil {
		return nil, "", err
	}
	newest := newestBySlug(rows)
	slugs := make([]string, 0, len(newest))
	for slug, row := range newest {
		if servesSessionStart(row.metadata) {
			slugs = append(slugs, slug)
		}
	}
	slices.SortFunc(slugs, func(a, b string) int {
		return cmp.Or(
			cmp.Compare(pillOrder(newest[a].metadata), pillOrder(newest[b].metadata)),
			cmp.Compare(a, b))
	})
	return slugs, "roca", nil
}

// newestBySlug is the pill each slug is served from: the read comes back newest
// first, so a recompiled pill wins over the row it replaced. The claim is made
// by the newest row whether or not it serves the session start, which is what
// keeps turning a pill off from resurrecting the version before it.
func newestBySlug(rows []pillRow) map[string]pillRow {
	newest := map[string]pillRow{}
	for _, row := range rows {
		slug, _ := row.metadata["pill_slug"].(string)
		if _, claimed := newest[slug]; slug != "" && !claimed {
			newest[slug] = row
		}
	}
	return newest
}

// pills renders the requested slugs, in the order they were asked for. A
// recompiled pill is a newer row for the same slug, so the newest row wins.
func (s *Service) pills(ctx context.Context, slugs []string, project string) ([]string, error) {
	if len(slugs) == 0 {
		return nil, nil
	}
	rows, err := s.readPills(ctx, project)
	if err != nil {
		return nil, err
	}
	newest := newestBySlug(rows)
	var rendered []string
	for _, slug := range slugs {
		if row, ok := newest[slug]; ok {
			rendered = append(rendered, renderPill(row))
		}
	}
	return rendered, nil
}

type pillRow struct {
	content  string
	metadata map[string]any
}

// readPills reads the active pills of this scope, newest first. The global
// pills travel with the project's ones, because a pill with no project is one
// every session receives.
func (s *Service) readPills(ctx context.Context, project string) ([]pillRow, error) {
	physical := s.registry.Resolve("pill", "pill")
	reader, err := s.db.ReadOnly()
	if err != nil {
		return nil, err
	}
	scope, arguments := "project IS NULL", []any{physical}
	if project != "" {
		scope, arguments = "(project = ? OR project IS NULL)", []any{physical, project}
	}
	rows, err := reader.QueryContext(ctx,
		`SELECT content, metadata FROM memories
		 WHERE layer = ? AND status = 'active' AND json_valid(metadata) AND `+scope+`
		 ORDER BY created_at DESC, id DESC`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("read the pills: %w", err)
	}
	defer rows.Close()

	var out []pillRow
	for rows.Next() {
		var content, raw string
		if err := rows.Scan(&content, &raw); err != nil {
			return nil, err
		}
		metadata := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			continue // a row whose metadata is not an object carries no slug
		}
		out = append(out, pillRow{content: content, metadata: metadata})
	}
	return out, rows.Err()
}

// handoffs are the newest handoff lines of this scope. Unlike the pills they
// are strictly scoped: a handoff from another project is not this session's
// business.
func (s *Service) handoffs(ctx context.Context, project string, limit int) ([]string, error) {
	if limit < 1 {
		return nil, nil
	}
	physical := s.registry.Resolve("handoff", "handoff")
	reader, err := s.db.ReadOnly()
	if err != nil {
		return nil, err
	}
	condition, arguments := "project IS NULL", []any{physical}
	if project != "" {
		condition, arguments = "project = ?", []any{physical, project}
	}
	rows, err := reader.QueryContext(ctx,
		`SELECT content, created_at FROM memories
		 WHERE layer = ? AND status = 'active' AND `+condition+`
		 ORDER BY created_at DESC, id DESC LIMIT ?`,
		append(arguments, limit)...)
	if err != nil {
		return nil, fmt.Errorf("read the handoffs: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var content string
		var created sql.NullString
		if err := rows.Scan(&content, &created); err != nil {
			return nil, err
		}
		out = append(out, "["+created.String+"] "+content)
	}
	return out, rows.Err()
}

// renderPill renders one stored pill into the compact injected form: its title
// and the compiled points, or its content with the heading that only repeats
// the title dropped.
func renderPill(row pillRow) string {
	title, _ := row.metadata["pill_title"].(string)
	if title == "" {
		title, _ = row.metadata["pill_slug"].(string)
	}
	if title == "" {
		title = "pill"
	}
	if points, ok := row.metadata["compiled_points"].([]any); ok {
		var bullets []string
		for _, point := range points {
			if text := strings.TrimSpace(fmt.Sprint(point)); text != "" {
				bullets = append(bullets, "- "+text)
			}
		}
		if len(bullets) > 0 {
			return "[" + title + "]\n" + strings.Join(bullets, "\n")
		}
	}
	if body := withoutHeading(row.content, title); body != "" {
		return "[" + title + "]\n" + body
	}
	return "[" + title + "]"
}

// withoutHeading drops a heading that only repeats the title, and the source
// appendix a compiled pill carries.
func withoutHeading(text, title string) string {
	// Split never answers with an empty slice, so there is always a first line.
	lines := strings.Split(text, "\n")
	heading := strings.ToLower(strings.TrimSpace(strings.TrimLeft(lines[0], "# ")))
	wanted := strings.ToLower(strings.TrimSpace(title))
	if heading == wanted || (wanted != "" && strings.Contains(heading, wanted)) {
		text = strings.TrimLeft(strings.Join(lines[1:], "\n"), "\n")
	}
	return strings.TrimSpace(strings.SplitN(text, sourceMemoriesHead, 2)[0])
}

func servesSessionStart(metadata map[string]any) bool {
	value, declared := metadata["session_start"]
	if !declared {
		return true
	}
	if text, ok := value.(string); ok {
		return !falseWords[strings.ToLower(strings.TrimSpace(text))]
	}
	served, ok := value.(bool)
	return !ok || served
}

var falseWords = map[string]bool{"": true, "0": true, "false": true, "no": true, "off": true}

func pillOrder(metadata map[string]any) int {
	switch value := metadata["pill_order"].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return 1_000_000
	}
}
