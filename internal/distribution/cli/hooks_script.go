package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

// This file owns the harnesses whose session hook is a program rather than a
// configuration entry: pi loads TypeScript extensions, OpenCode loads
// JavaScript plugins. La Roca writes one file of its own into the directory
// each harness already scans and never reads, moves, or rewrites its
// neighbours, so every extension and plugin another tool installed there stays
// exactly as it is. The whole file is the SYSTEM fragment; there is no USER zone
// inside it. Force bypasses the edit guard, not conditional publication.

// rocaScriptMarker is the ownership line every script this product writes
// carries. A file in the same directory without it was written by someone else
// and is never replaced or removed.
const rocaScriptMarker = "ROCA_MANAGED_HOOK=session"

const scriptHookTimeoutMs = 15000

// sessionScript renders the extension or plugin for one runtime, wired to the
// exact binary and flags the install was asked for.
func sessionScript(runtime, executable string, req sessionRequest) string {
	quoted := make([]string, 0, 6)
	for _, arg := range append([]string{
		"hooks", "run", "session", "--runtime", runtime,
	}, req.flags()...) {
		encoded, _ := json.Marshal(arg)
		quoted = append(quoted, string(encoded))
	}
	args := "[" + strings.Join(quoted, ", ") + "]"
	binary, _ := json.Marshal(executable)
	header := fmt.Sprintf(`// installed by La Roca
// managed by `+"`roca hooks install %s`"+`; reinstalling overwrites this file.
// Add your own extensions beside this file instead of editing it.
// %s
`, runtime, rocaScriptMarker)
	reader := fmt.Sprintf(`import { execFile } from "node:child_process";

const ROCA = %s;
const ROCA_ARGS = %s;

function rocaSessionContext(cwd) {
  return new Promise((resolve) => {
    execFile(
      ROCA,
      ROCA_ARGS,
      { cwd: cwd || process.cwd(), timeout: %d, maxBuffer: 8 * 1024 * 1024 },
      (error, stdout) => resolve(error ? "" : String(stdout).trim()),
    );
  });
}
`, binary, args, scriptHookTimeoutMs)

	if runtime == agentcfg.RuntimeOpencode {
		return header + "\n" + reader + `
export const RocaSessionContextPlugin = async ({ directory }) => {
  const bySession = new Map();

  return {
    "experimental.chat.system.transform": async (input, output) => {
      const sessionID = input.sessionID ?? "__global__";
      let body = bySession.get(sessionID);
      if (body === undefined) {
        body = await rocaSessionContext(directory);
        bySession.set(sessionID, body);
      }
      if (body.length === 0) return;
      output.system.push(body);
    },
  };
};
`
	}
	return header + "// @ts-nocheck\n\n" + reader + `
export default function (pi) {
  let pending;
  let injected = false;

  pi.on("session_start", (_event, ctx) => {
    injected = false;
    pending = rocaSessionContext(ctx?.cwd);
  });

  // A session's context is injected once, on its first turn: pi has no event
  // that adds a message before the operator has asked for anything.
  pi.on("before_agent_start", async () => {
    if (injected) return;
    injected = true;
    const body = await (pending ?? rocaSessionContext());
    if (!body) return;
    return {
      message: { customType: "roca-session", content: body, display: false },
    };
  });
}
`
}

// installSessionScript writes one runtime's extension or plugin. A file this
// product did not write is never replaced; one it wrote and the operator edited
// requires `--force` to pass the edit guard. Publication still follows
// securefile.Replace's contract.
//
// The registry is read before anything is written, because an install that
// wrote the script and then failed its own bookkeeping would leave a working
// hook behind a non-zero exit, which is the one outcome an operator cannot act
// on.
func installSessionScript(env *cliEnv, runtime, path, executable string,
	req sessionRequest, force bool) (agentcfg.Outcome, string, error) {
	outcome := agentcfg.Outcome{Runtime: runtime, Path: path}
	entry, registered, err := env.registeredArtifact(artifactKindHook, runtime, path)
	if err != nil {
		return outcome, "", err
	}
	desired := sessionScript(runtime, executable, req)
	previous, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return outcome, "", fmt.Errorf("read %s: %w", path, err)
	case !strings.Contains(string(previous), rocaScriptMarker):
		return outcome, "", fmt.Errorf(
			"refuse to replace %s, which La Roca did not write", path)
	case string(previous) == desired:
		return outcome, "", env.registerHook(path, runtime, desired)
	case !force && (!registered || entry.SystemSHA256 != artifact.Checksum(string(previous))):
		// The whole script is the SYSTEM fragment, so a file that no longer
		// says what the install recorded is the operator's edit, and a file no
		// registry entry stands behind was never proven to be this build's.
		return outcome, fmt.Sprintf("warning: %s has edits; run `roca hooks install %s "+
			"--force` to replace it", path, runtime), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return outcome, "", fmt.Errorf("create the directory of %s: %w", path, err)
	}
	if previous != nil {
		backup, err := securefile.BackUp(path, previous)
		if err != nil {
			return outcome, "", err
		}
		outcome.Backup = backup
		if err := securefile.Replace(path, []byte(desired), previous); err != nil {
			return outcome, "", err
		}
	} else if err := securefile.Write(path, []byte(desired), 0o600, 0o700); err != nil {
		return outcome, "", err
	}
	outcome.Changed = true
	return outcome, "", env.registerHook(path, runtime, desired)
}

// uninstallSessionScript removes the file this product wrote and nothing else.
// A file that is not there is not an error, and one without the ownership line
// is left where it is with a warning naming it.
func uninstallSessionScript(env *cliEnv, runtime, path string) (agentcfg.Outcome, string, error) {
	outcome := agentcfg.Outcome{Runtime: runtime, Path: path}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return outcome, "", env.unregisterArtifact(artifactKindHook, runtime, path)
	}
	if err != nil {
		return outcome, "", fmt.Errorf("read %s: %w", path, err)
	}
	if !strings.Contains(string(body), rocaScriptMarker) {
		return outcome, fmt.Sprintf("warning: %s was not written by La Roca, "+
			"so nothing there was changed", path), nil
	}
	if err := os.Remove(path); err != nil {
		return outcome, "", fmt.Errorf("remove %s: %w", path, err)
	}
	outcome.Changed = true
	return outcome, "", env.unregisterArtifact(artifactKindHook, runtime, path)
}
