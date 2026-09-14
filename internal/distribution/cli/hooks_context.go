package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// sessionCraft is the fixed SYSTEM fragment every runtime's session hook
// injects, word for word. A harness that opens without it investigates La Roca
// by improvising: the whole point of installing the same hook everywhere is
// that the craft arrives with the memory, in one wording, on every harness.
const sessionCraft = `## La Roca is this machine's agent memory
- Vectors first, no inference: ` + "`" + `roca vector query "<phrase in your own words>" 20..100 --databases corpus,ops` + "`" + ` to find the nearby rows.
- Then frame it in SQL yourself: load the ` + "`" + `roca-semantica` + "`" + ` skill and run the SELECT with ` + "`" + `roca exec` + "`" + `. Qualify every table (` + "`" + `plugin_roca_corpus.exchanges` + "`" + `, ` + "`" + `plugin_roca_ops.memories` + "`" + `); an unqualified table returns 0 rows without a warning.
- ` + "`" + `roca query` + "`" + ` is the zero-inference hybrid search: use it only when you cannot write the SELECT. ` + "`" + `roca playground` + "`" + ` and ` + "`" + `roca explore` + "`" + ` spend inference and are last resort. Never pass ` + "`" + `--full` + "`" + `.
- Everything goes through the ` + "`" + `roca` + "`" + ` binary. Never open the .db files with sqlite3 or python.`

// sessionRequest is what one installed hook was asked to inject. The SYSTEM
// fragment is not a member because it is not optional: it is what every
// installed hook carries whatever else the operator asked for.
type sessionRequest struct {
	pills   bool
	handoff bool
}

func (r sessionRequest) flags() []string {
	var flags []string
	if r.pills {
		flags = append(flags, "--pills")
	}
	if r.handoff {
		flags = append(flags, "--handoff")
	}
	return flags
}

// sessionHookBody renders what the hook injects: the fixed fragment, then the
// active pills, then the latest handoff, each only when it has something to
// say. A database this hook cannot open is not a reason to inject nothing: the
// craft still arrives, which is the part a fresh session cannot look up.
func sessionHookBody(ctx context.Context, env *cliEnv, req sessionRequest) string {
	sections := []string{sessionCraft}
	if req.pills {
		if rendered := sessionSection(ctx, env,
			(*service.Service).ListPills, axi.Pills); rendered != "" {
			sections = append(sections, rendered)
		}
	}
	if req.handoff {
		if rendered := sessionSection(ctx, env, latestHandoffLoader(1),
			func(list service.HandoffList) string {
				return axi.HandoffHeads(list, claudeHandoffHeadChars)
			}); rendered != "" {
			sections = append(sections, rendered)
		}
	}
	return strings.Join(sections, "\n\n")
}

// sessionSection renders one session-context view for the project the hook was
// launched in, and renders nothing at all when the database cannot answer. A
// hook is not a place to report a broken read: the harness would show the
// operator a stack trace where their project context belongs.
func sessionSection[T any](ctx context.Context, env *cliEnv,
	load func(*service.Service, context.Context, string) (T, error), render func(T) string) string {
	project, err := resolveProject("")
	if err != nil {
		return ""
	}
	svc, _, err := env.openSessionContextService()
	if err != nil {
		return ""
	}
	defer svc.Close()
	result, err := load(svc, ctx, project)
	if err != nil {
		return ""
	}
	return strings.TrimRight(render(result), "\n")
}

// sessionHookStdout wraps the body in the envelope the runtime reads. Each
// harness names the same idea differently and discards what it cannot parse,
// so the envelope is the one thing that is deliberately not shared.
func sessionHookStdout(runtime, body string) string {
	body = strings.TrimSpace(body)
	switch runtime {
	case agentcfg.RuntimePi, agentcfg.RuntimeOpencode:
		// The extension and the plugin read stdout and inject it themselves.
		if body == "" {
			return ""
		}
		return body + "\n"
	case agentcfg.RuntimeCursor:
		return jsonLine(map[string]any{"additional_context": body}, body)
	case agentcfg.RuntimeZcode:
		return jsonLine(map[string]any{"additionalContext": body}, body)
	default:
		// Claude Code and Codex share one SessionStart output contract.
		return jsonLine(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": claudeSessionStartEvent, "additionalContext": body,
		}}, body)
	}
}

func jsonLine(envelope map[string]any, body string) string {
	if body == "" {
		return "{}\n"
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "{}\n"
	}
	return string(encoded) + "\n"
}

func runSessionHook(ctx context.Context, env *cliEnv, runtime string, req sessionRequest) error {
	if err := supportedHookRuntime(runtime); err != nil {
		return err
	}
	fmt.Fprint(env.out, sessionHookStdout(runtime, sessionHookBody(ctx, env, req)))
	return nil
}
