package service_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestStoreRefusesAHandoffFromANonSessionWriter(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	content := shapedHandoff("branch-shaped worker noise")

	tests := []struct {
		name    string
		request service.StoreRequest
		agent   string
		surface string
		origin  string
	}{
		{"unknown authorship", service.StoreRequest{Layer: "handoff", Content: content},
			"unknown", "unknown", "agent"},
		{"cron origin", service.StoreRequest{
			Layer: "handoff", Content: content, Origin: "cron",
			Authorship: service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceCLI},
		}, "claude", service.SurfaceCLI, "cron"},
		{"plugin origin", service.StoreRequest{
			Layer: "handoff", Content: content, Origin: "plugin:demo",
			Authorship: service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceCLI},
		}, "claude", service.SurfaceCLI, "plugin:demo"},
		{"unknown surface", service.StoreRequest{
			Layer: "handoff", Content: content,
			Authorship: service.Authorship{Agent: "claude", Model: "sonnet"},
		}, "claude", "unknown", "agent"},
		{"worker-named agent", service.StoreRequest{
			Layer: "handoff", Content: content,
			Authorship: service.Authorship{
				Agent: "glm-5.2 (codex/slopslint-detector-a1)", Model: "glm", Surface: service.SurfaceCLI,
			},
		}, "glm-5.2 (codex/slopslint-detector-a1)", service.SurfaceCLI, "agent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.Store(t.Context(), test.request)
			if err == nil {
				t.Fatal("store accepted a handoff from a writer that is not a session harness")
			}
			got := err.Error()
			wantPrefix := fmt.Sprintf("handoff refused: agent=%q surface=%q origin=%q",
				test.agent, test.surface, test.origin)
			if !strings.Contains(got, wantPrefix) {
				t.Errorf("refusal does not name what it saw (%s): %v", wantPrefix, err)
			}
			for _, want := range []string{
				"session writers are", "tasks-axi", "pr", "decision", "expires_at",
				"accepted layers for this surface", "layer=discovery",
				"branch/scope:", "done:", "state:", "next:", "layer=handoff",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("refusal does not name %q: %v", want, err)
				}
			}
		})
	}
}

func TestStoreRefusesAHandoffThatOmitsTheRequiredShape(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	tests := []struct {
		name    string
		content string
	}{
		{"unlabeled prose", "token refresh done, retry pending"},
		{"near labels", "DONE (verified): recorded\nSTATUS: stored\nSUPERSEDE 42\nbranch: fixture\nnext: continue"},
		{"next step alias", "branch: fixture\ndone: recorded\nstate: stored\nnext step: ship"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.Store(t.Context(), service.StoreRequest{
				Layer: "handoff", Content: test.content, Authorship: sessionWriter(),
			})
			if err == nil {
				t.Fatal("store accepted a handoff without the accepted labels")
			}
			for _, want := range []string{
				"branch/scope:", "done:", "state:", "current state:", "next:",
				"SUPERSEDE", "--supersedes",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("shape refusal does not name %q: %v", want, err)
				}
			}
		})
	}
}

func TestStoreRefusesAHandoffWithBlankLabeledFields(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	_, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "handoff", Content: "branch: done: state: next:", Authorship: sessionWriter(),
	})
	if err == nil {
		t.Fatal("store accepted blank handoff fields")
	}
	for _, want := range []string{"branch/scope", "done", "state", "next"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("blank-field refusal does not name %q: %v", want, err)
		}
	}
}

func TestStoreAcceptsASessionHandoffWithTheRequiredShape(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	result, err := svc.Store(t.Context(), sessionHandoff("the session closed on this branch"))
	if err != nil {
		t.Fatal(err)
	}
	if result.ID == 0 || result.Layer != "handoff" {
		t.Fatalf("accepted write = %+v", result)
	}
}

func TestCanonicalSessionAgentMapsRuntimeAliases(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"claude-desktop", "claude"},
		{"cowork", "claude"},
		{"Claude Code", "claude"},
		{"claude-ai", "claude"},
		{"claude-code", "claude"},
		{"codex", "codex"},
		{"Codex CLI", "codex"},
		{"hermes-agent", "hermes"},
		{"ZCode", "zcode"},
		{"cursor-ide", "cursor"},
		{"qwen-code", "qwen"},
		{"glm-5.2 (codex/slopslint-detector-a1)", "glm-5.2 (codex/slopslint-detector-a1)"},
		{"", ""},
	}
	for _, test := range tests {
		if got := service.CanonicalSessionAgent(test.in); got != test.want {
			t.Errorf("CanonicalSessionAgent(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestStoreAcceptsHandoffsFromAliasedSessionWriters(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	content := shapedHandoff("aliased session writer")
	for _, agent := range []string{
		"claude-desktop", "cowork", "Claude Code", "claude-ai", "codex",
		"ZCode", "zcode", "opencode", "hermes", "pi", "cursor", "grok", "qwen",
	} {
		t.Run(agent, func(t *testing.T) {
			result, err := svc.Store(t.Context(), service.StoreRequest{
				Layer: "handoff", Content: content + "\n" + agent,
				Authorship: service.Authorship{Agent: agent, Model: "sonnet", Surface: service.SurfaceMCP},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.ID == 0 || result.Layer != "handoff" {
				t.Fatalf("accepted write = %+v", result)
			}
		})
	}
}

func TestMCPLegacyAliasRetryIsTheSameMemory(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	content := "legacy MCP alias retry fixture"
	first, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "discovery", Content: content,
		Authorship: service.Authorship{Agent: "Claude Code", Model: "sonnet", Surface: service.SurfaceMCP},
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "discovery", Content: content,
		Authorship: service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceMCP},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Skipped || retry.ID != first.ID {
		t.Fatalf("MCP alias retry = %+v, want skipped id %d", retry, first.ID)
	}
}

func TestCLIHarnessNamesStayDistinctAuthors(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	content := "cli harness names stay distinct"
	first, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "discovery", Content: content,
		Authorship: service.Authorship{Agent: "claude-code", Model: "sonnet", Surface: service.SurfaceCLI},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "discovery", Content: content,
		Authorship: service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceCLI},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Skipped || second.ID == first.ID {
		t.Fatalf("CLI harness names collapsed: first=%d second=%+v", first.ID, second)
	}
}

func TestStoreAppliesTheHandoffPolicyThroughTheHandoverAlias(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	_, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "handover", Content: "an alias of handoff",
	})
	if err == nil {
		t.Fatal("handover alias skipped the handoff writer policy")
	}
}

func TestStoreAutoSupersedesThePreviousCurrentHandoffForAProject(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	ctx := t.Context()

	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{"same project without supersedes", func(t *testing.T) {
			first := mustStoreHandoff(t, svc, "la-roca-e2e-handoff-auto", "handoff auto A")
			second := mustStoreHandoff(t, svc, "la-roca-e2e-handoff-auto", "handoff auto B")
			if second.ID == first.ID {
				t.Fatal("second store reused the first id")
			}
			got := memorySupersedes(t, svc, second.ID)
			if !got.Valid || got.Int64 != first.ID {
				t.Fatalf("second supersedes = %+v, want %d", got, first.ID)
			}
			if n := currentHandoffCount(t, svc, "la-roca-e2e-handoff-auto"); n != 1 {
				t.Fatalf("current handoffs = %d, want 1", n)
			}
		}},
		{"repairs pre-existing multiple current heads", func(t *testing.T) {
			project := "repair-handoff"
			first := insertHandoffRow(t, svc, project, "repair A", "2026-08-01 00:00:00", 0)
			second := insertHandoffRow(t, svc, project, "repair B", "2026-08-02 00:00:00", 0)
			third := mustStoreHandoff(t, svc, project, "repair C")
			if got := memorySupersedes(t, svc, third.ID); !got.Valid || got.Int64 != second {
				t.Fatalf("new handoff supersedes = %+v, want %d", got, second)
			}
			if got := memorySupersedes(t, svc, first); got.Valid {
				t.Fatalf("repaired handoff predecessor changed = %+v", got)
			}
			if !memoryIsSuperseded(t, svc, first) {
				t.Fatalf("older handoff %d is still current", first)
			}
			if n := currentHandoffCount(t, svc, project); n != 1 {
				t.Fatalf("repaired current handoffs = %d, want 1", n)
			}
		}},
		{"forked current heads keep their predecessors", func(t *testing.T) {
			project := "fork-handoff"
			ancestorA := insertHandoffRow(t, svc, project, "ancestor A", "2026-08-01 00:00:00", 0)
			ancestorB := insertHandoffRow(t, svc, project, "ancestor B", "2026-08-02 00:00:00", 0)
			first := insertHandoffRow(t, svc, project, "fork A", "2026-08-03 00:00:00", ancestorA)
			second := insertHandoffRow(t, svc, project, "fork B", "2026-08-04 00:00:00", ancestorB)
			third := mustStoreHandoff(t, svc, project, "fork C")
			if got := memorySupersedes(t, svc, third.ID); !got.Valid || got.Int64 != second {
				t.Fatalf("new handoff supersedes = %+v, want %d", got, second)
			}
			if got := memorySupersedes(t, svc, second); !got.Valid || got.Int64 != ancestorB {
				t.Fatalf("fork head predecessor overwritten: %+v, want %d", got, ancestorB)
			}
			if got := memorySupersedes(t, svc, first); !got.Valid || got.Int64 != ancestorA {
				t.Fatalf("other fork head predecessor overwritten: %+v, want %d", got, ancestorA)
			}
			if !memoryIsSuperseded(t, svc, first) {
				t.Fatalf("older fork head %d is still current", first)
			}
			if !memoryIsSuperseded(t, svc, ancestorA) || !memoryIsSuperseded(t, svc, ancestorB) {
				t.Fatal("rewriting the fork resurrected an ancestor")
			}
			if n := currentHandoffCount(t, svc, project); n != 1 {
				t.Fatalf("fork current handoffs = %d, want 1", n)
			}
			if n := issueCurrentHandoffCount(t, svc, project); n != 1 {
				t.Fatalf("issue non-superseded count = %d, want 1", n)
			}
			report, err := svc.Health(ctx, service.HealthRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if report.Checks["runtime_layers_not_in_registry"].Status != service.HealthPass {
				t.Fatalf("runtime layer health = %+v", report.Checks["runtime_layers_not_in_registry"])
			}
		}},
		{"inactive handoffs do not retire the active head", func(t *testing.T) {
			for _, status := range []string{"pending", "resolved"} {
				t.Run(status, func(t *testing.T) {
					project := "inactive-" + status
					active := mustStoreHandoff(t, svc, project, "active")
					request := sessionHandoff(status)
					request.Project, request.Status = project, status
					stored, err := svc.Store(ctx, request)
					if err != nil {
						t.Fatal(err)
					}
					if got := memorySupersedes(t, svc, stored.ID); got.Valid {
						t.Fatalf("%s handoff supersedes = %+v", status, got)
					}
					if got := memorySupersedes(t, svc, active.ID); got.Valid {
						t.Fatalf("active handoff was superseded by %s: %+v", status, got)
					}
				})
			}
		}},
		{"project identity stays exact", func(t *testing.T) {
			mustStoreHandoff(t, svc, "trimmed-handoff", "trimmed A")
			second := sessionHandoff("trimmed B")
			second.Project = " trimmed-handoff "
			stored, err := svc.Store(ctx, second)
			if err != nil {
				t.Fatal(err)
			}
			if got := memorySupersedes(t, svc, stored.ID); got.Valid {
				t.Fatalf("distinct project identity supersedes = %+v", got)
			}
			var project string
			if err := svc.DB().SQL().QueryRow("SELECT project FROM memories WHERE id = ?", stored.ID).Scan(&project); err != nil {
				t.Fatal(err)
			}
			if project != " trimmed-handoff " {
				t.Fatalf("stored project = %q, want exact identity", project)
			}
		}},
		{"other project stays current", func(t *testing.T) {
			alpha := mustStoreHandoff(t, svc, "alpha-handoff", "alpha first")
			mustStoreHandoff(t, svc, "beta-handoff", "beta first")
			if got := memorySupersedes(t, svc, alpha.ID); got.Valid {
				t.Fatalf("other-project handoff was superseded: %+v", got)
			}
		}},
		{"explicit supersedes is kept", func(t *testing.T) {
			first := mustStoreHandoff(t, svc, "explicit-handoff", "explicit A")
			second := mustStoreHandoff(t, svc, "explicit-handoff", "explicit B")
			third := sessionHandoff("explicit C")
			third.Project = "explicit-handoff"
			third.Supersedes = first.ID
			got, err := svc.Store(ctx, third)
			if err != nil {
				t.Fatal(err)
			}
			pointed := memorySupersedes(t, svc, got.ID)
			if !pointed.Valid || pointed.Int64 != first.ID {
				t.Fatalf("explicit supersedes = %+v, want %d (not auto %d)", pointed, first.ID, second.ID)
			}
			if n := currentHandoffCount(t, svc, "explicit-handoff"); n != 1 {
				t.Fatalf("explicit current handoffs = %d, want 1", n)
			}
		}},
		{"auto supersede chooses the normalized newest head", func(t *testing.T) {
			project := "timestamp-handoff"
			first := insertHandoffRow(t, svc, project, "timestamp A", "2026-01-01 00:30:00", 0)
			insertHandoffRow(t, svc, project, "timestamp B", "2026-01-01T01:00:00+02:00", 0)
			third := mustStoreHandoff(t, svc, project, "timestamp C")
			if got := memorySupersedes(t, svc, third.ID); !got.Valid || got.Int64 != first {
				t.Fatalf("normalized newest supersedes = %+v, want %d", got, first)
			}
		}},
		{"non-handoff layer is not auto-superseded", func(t *testing.T) {
			first, err := svc.Store(ctx, service.StoreRequest{
				Layer: "discovery", Project: "layer-guard", Content: "discovery A",
			})
			if err != nil {
				t.Fatal(err)
			}
			second, err := svc.Store(ctx, service.StoreRequest{
				Layer: "discovery", Project: "layer-guard", Content: "discovery B",
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := memorySupersedes(t, svc, second.ID); got.Valid {
				t.Fatalf("discovery store auto-superseded: %+v first=%d", got, first.ID)
			}
		}},
		{"project-less handoffs stay independent", func(t *testing.T) {
			first, err := svc.Store(ctx, sessionHandoff("global A"))
			if err != nil {
				t.Fatal(err)
			}
			second, err := svc.Store(ctx, sessionHandoff("global B"))
			if err != nil {
				t.Fatal(err)
			}
			if got := memorySupersedes(t, svc, second.ID); got.Valid {
				t.Fatalf("global handoff auto-superseded: %+v first=%d", got, first.ID)
			}
			if n := currentHandoffCount(t, svc, ""); n < 2 {
				t.Fatalf("global currents = %d, want both", n)
			}
		}},
		{"retry of the current handoff is skipped", func(t *testing.T) {
			first := mustStoreHandoff(t, svc, "retry-handoff", "same body")
			retry := sessionHandoff("same body")
			retry.Project = "retry-handoff"
			got, err := svc.Store(ctx, retry)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Skipped || got.ID != first.ID {
				t.Fatalf("retry = %+v, want skipped id %d", got, first.ID)
			}
		}},
		{"retry repairs all current heads before skipping", func(t *testing.T) {
			project := "retry-repair-handoff"
			first := insertHandoffRow(t, svc, project, "retry A", "2026-08-01 00:00:00", 0)
			second := insertHandoffRow(t, svc, project, "retry B", "2026-08-02 00:00:00", 0)
			retry := sessionHandoff("retry B")
			retry.Project = project
			got, err := svc.Store(ctx, retry)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Skipped || got.ID != second {
				t.Fatalf("retry = %+v, want skipped id %d", got, second)
			}
			if memorySupersedes(t, svc, first).Valid {
				t.Fatal("repair overwrote the older handoff predecessor")
			}
			if !memoryIsSuperseded(t, svc, first) {
				t.Fatalf("older current handoff %d was not retired", first)
			}
			if n := currentHandoffCount(t, svc, project); n != 1 {
				t.Fatalf("current handoffs = %d, want 1", n)
			}
		}},
		{"handover alias auto-supersedes", func(t *testing.T) {
			first := sessionHandoff("alias A")
			first.Layer = "handover"
			first.Project = "alias-handoff"
			stored, err := svc.Store(ctx, first)
			if err != nil {
				t.Fatal(err)
			}
			second := sessionHandoff("alias B")
			second.Layer = "handover"
			second.Project = "alias-handoff"
			got, err := svc.Store(ctx, second)
			if err != nil {
				t.Fatal(err)
			}
			pointed := memorySupersedes(t, svc, got.ID)
			if !pointed.Valid || pointed.Int64 != stored.ID {
				t.Fatalf("handover alias supersedes = %+v, want %d", pointed, stored.ID)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func mustStoreHandoff(t *testing.T, svc *service.Service, project, body string) service.StoreResult {
	t.Helper()
	req := sessionHandoff(body)
	req.Project = project
	result, err := svc.Store(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID == 0 || result.Skipped {
		t.Fatalf("store %q = %+v", body, result)
	}
	return result
}

func memorySupersedes(t *testing.T, svc *service.Service, id int64) sql.NullInt64 {
	t.Helper()
	var value sql.NullInt64
	if err := svc.DB().SQL().QueryRow(`SELECT supersedes FROM memories WHERE id = ?`, id).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func insertHandoffRow(t *testing.T, svc *service.Service, project, body, createdAt string, supersedes int64) int64 {
	t.Helper()
	result, err := svc.DB().SQL().Exec(
		`INSERT INTO memories (layer, content, metadata, origin, source_agent, source_model, source_surface, project, status, supersedes, created_at)
		 VALUES ('handoff', ?, '{}', 'agent', 'claude', 'sonnet', 'cli', ?, 'active', ?, ?)`,
		strings.TrimSpace(shapedHandoff(body)), project, orNullInt64(supersedes), createdAt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func orNullInt64(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func memoryIsSuperseded(t *testing.T, svc *service.Service, id int64) bool {
	t.Helper()
	var n int
	if err := svc.DB().SQL().QueryRow(`SELECT COUNT(*) FROM memories WHERE supersedes = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func issueCurrentHandoffCount(t *testing.T, svc *service.Service, project string) int {
	t.Helper()
	var n int
	if err := svc.DB().SQL().QueryRow(`SELECT COUNT(*) FROM memories
		WHERE project = ? AND layer = 'handoff'
		  AND id NOT IN (SELECT supersedes FROM memories WHERE supersedes IS NOT NULL)`, project).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func currentHandoffCount(t *testing.T, svc *service.Service, project string) int {
	t.Helper()
	query := `SELECT COUNT(*) FROM memories
		WHERE layer = 'handoff' AND status = 'active'
		  AND id NOT IN (SELECT supersedes FROM memories WHERE supersedes IS NOT NULL)`
	var args []any
	if project == "" {
		query += " AND project IS NULL"
	} else {
		query += " AND project = ?"
		args = append(args, project)
	}
	var n int
	if err := svc.DB().SQL().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sessionWriter() service.Authorship {
	return service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceCLI}
}

func shapedHandoff(body string) string {
	return strings.TrimSpace(body) + "\nbranch: fixture\ndone: recorded\nstate: stored\nnext: continue\n"
}

func sessionHandoff(body string) service.StoreRequest {
	return service.StoreRequest{
		Layer: "handoff", Content: shapedHandoff(body), Authorship: sessionWriter(),
	}
}
