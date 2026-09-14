# Session hooks: what a live session of each harness actually received

Headless tests prove this product writes the entry it meant to write. They
cannot prove the harness reads it. This run installed the hooks on one machine
and opened a real session of each harness in its own terminal pane, then read
back what that harness recorded as its own context.

Binary under test: this branch, built from source. Every install used
`roca hooks install <runtime> --pills --handoff`, into homes that already held
four to six other tools' session hooks. None of them was moved, rewritten or
removed by any install or withdrawal in this run.

## What each harness recorded

| Runtime | Session opened | Where the context was found | Fragment | Pills | Handoff |
| --- | --- | --- | --- | --- | --- |
| claude | `claude -p` | transcript `attachment.hook_success` for `SessionStart:startup`, stdout parsed as `hookSpecificOutput.additionalContext` | yes | yes | yes |
| codex | `codex exec` | rollout `response_item` message, role `developer`, envelope unwrapped by Codex | yes | yes | yes |
| cursor | `cursor-agent --trust -p` | chat store, inside the hooks-context block Cursor builds from `additional_context` | yes | yes | yes |
| pi | `pi -p` | session `custom_message` with `customType: "roca-session"` | yes | yes | yes |
| opencode | `opencode run` | the model quoted the fragment's first line back from its own system prompt | yes | n/a | n/a |
| zcode | wrapper only — see below | wrapper stdout `{"additionalContext": …}` | yes | yes | yes |

Codex records the injected text with its own truncation notice
(`Warning: truncated output (original token count: 9192)`), which is what it
does to every large hook output, La Roca's included. A machine whose pills are
large gets the head of them, not nothing.

## Two things a live session found that no headless test could

**Codex will not run a hook it has not been trusted with, and says so to
nobody.** The first live Codex session ran four SessionStart hooks and skipped
the fifth — ours — in complete silence. Codex records a `trusted_hash` per hook
entry under `[hooks.state]` in its own `config.toml`, and a newly written entry
has none. Nothing in the install output, the hooks file, or the session said so.
`roca hooks install codex` now prints that one remaining step. Trusting a hook
is the operator's decision and this product never writes that approval on their
behalf; the lab runs below used Codex's own
`--dangerously-bypass-hook-trust` flag, which exists for automation that vetted
the hook itself.

Once trusted, Codex parses the `hookSpecificOutput.additionalContext` envelope
and injects the unwrapped text as a developer message, so the envelope Claude
Code uses is the right one for Codex too.

**An install must read its registry before it writes.** The machine used here
had a root-owned `~/.roca/artifacts.json` from an earlier privileged run. The
first pi install wrote its extension and then failed reading that registry,
leaving a working hook behind a non-zero exit — the one outcome an operator
cannot act on. The registry is now read first, so an unreadable one refuses
before anything is written. There is a test for it.

## ZCode

ZCode's session hooks could not be exercised end to end on this machine: its
`@zcode/tui` package is not installed, so the TUI refuses to start, and its
`-p` mode runs no SessionStart hooks. ZCode's transport is unchanged by this
work — the same nested `hooks.events.SessionStart` entry and the same wrapper
script — and both were verified directly: the installed wrapper is executable,
carries its ownership marker, and prints
`{"additionalContext": "## La Roca is this machine's agent memory…"}` with the
project's pills and latest handoff in it. Its config edit, container ownership
and withdrawal keep their existing unit coverage.

## Afterwards

Every hook installed for this run was withdrawn again, and each file was
compared byte for byte against the copy taken before the first install.
