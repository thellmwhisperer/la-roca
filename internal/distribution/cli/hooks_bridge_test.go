package cli

import (
	"context"
	"os"

	"github.com/thellmwhisperer/la-roca/internal/distribution/cli/hooks"
)

// The hooks suites drive the whole command line, so they stay in this package
// and reach the hooks package through these names.

const (
	rocaScriptMarker       = hooks.ScriptMarker
	zcodeHookWrapperMarker = hooks.ZcodeWrapperMarker
)

var (
	shellQuote        = hooks.ShellQuote
	hookArtifactPath  = hooks.ArtifactPath
	sessionHookStdout = hooks.SessionHookStdout
	zcodeHookJSON     = hooks.ZcodeHookJSON
	zcodeOwnsWrapper  = hooks.ZcodeOwnsWrapper
)

type sessionRequest struct{ pills, handoff bool }

func (r sessionRequest) request() hooks.SessionRequest {
	return hooks.SessionRequest{Pills: r.pills, Handoff: r.handoff}
}

func sessionHookCommand(executable, runtime string, req sessionRequest) string {
	return hooks.SessionHookCommand(executable, runtime, req.request())
}

func sessionHookBody(ctx context.Context, env *cliEnv, req sessionRequest) string {
	return hooks.SessionHookBody(ctx, env.hooksEnv(), req.request())
}

func zcodeWrapper(executable string, req sessionRequest) string {
	return hooks.ZcodeWrapper(executable, req.request())
}

func claudePillsHookCommand(executable string) string {
	return shellQuote(executable) + " hooks run claude-pills"
}

func claudeHandoffHookCommand(executable string) string {
	return shellQuote(executable) + " hooks run claude-handoff"
}

func stringMember(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

type zcodeWrapperState struct {
	hooks.ZcodeWrapperState
	body []byte
}

func readZcodeWrapperState(path string) (zcodeWrapperState, error) {
	state, err := hooks.ReadZcodeWrapperState(path)
	body, _ := os.ReadFile(path)
	return zcodeWrapperState{state, body}, err
}

func writeZcodeWrapper(path, content string, previous zcodeWrapperState) (string, error) {
	return hooks.WriteZcodeWrapper(path, content, previous.ZcodeWrapperState)
}
