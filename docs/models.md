# Model providers

After [installation](lifecycle.md#install), La Roca uses **detected agent CLIs
with a local floor**. It finds shipped CLI presets on `PATH`, uses their
existing signed-in sessions in stable order (`claude`, then `codex`), then
tries local Ollama. No La Roca login or provider table is required: ask with
`roca query`. An explicit provider order remains authoritative.

Questions use the first configured provider that reports itself ready. If the
selected provider is unavailable or produces unusable SQL, La Roca reports the
degraded state and attempts literal search over the local index.

## The configuration

One file, TOML, at `config.toml` inside the data directory (`~/.roca/`).
`roca doctor` prints the exact path it read.
Most installations do not need this file for model access: the examples below
are overrides for operators who want a fixed order, split inference, or a
fallback provider.

An interactive `roca init` is the shortest way to create those overrides. It
starts with models rather than provider names: detected agent CLIs are grouped
as origins with their shipped default and a free-text option, while Ollama
contributes the models returned by its local `/api/tags` catalogue. After the
model choice, init auto-selects its harness when exactly one origin matches or
asks which harness to use when several match. The confirmed pair is probed when
it differs from the already-ready default, then `models.order` and
`models.<provider>.model` are edited in place. Existing files keep their
comments and unrelated settings and receive a named recovery backup when the
edit changes them. The complete question and automation contracts live in
[Initialize](lifecycle.md#initialize).

Plain Enter through the chooser preserves the effective selection the normal
factory ordering would have made. Non-terminal init does not run this chooser
or write the model configuration; it reports the effective provider/model and
the configuration path once so automation stays question-free.

```toml
[models]
# The order they are tried in. The first available one serves.
order = ["codex", "deepseek", "ollama"]
# Optional. The order the RESULT ROWS are read by, when you want the two
# inferences on different providers. Leave it out and the provider that wrote
# the SQL also reads the rows, which is what every installation did before this
# key existed.
interpret_order = ["ollama"]
# Model budgets, in milliseconds. timeout_ms bounds a provider request;
# probe_ms bounds the availability question asked before every one.
timeout_ms = 90000
probe_ms   = 3000

[query]
# SQL that passed the read-only gate may execute for this long. The working
# default is 5000 ms when this section is absent.
timeout_ms = 5000

[models.codex]
model = "gpt-5.6-luna"

[models.claude]
# The built-in command uses Claude Code's existing signed-in session.
model = "sonnet"

[models.deepseek]
api_key = "sk-..."          # or leave it out and export DEEPSEEK_API_KEY
model   = "deepseek-chat"

[models.ollama]
base_url   = "http://localhost:11434"
model      = "qwen3.5:4b"
keep_alive = "10m"
# Off by default. A reasoning model's thinking is neither the SQL nor the
# summary asked of it, and on qwen3.5 leaving it on turned a local
# interpretation from seconds into minutes. Turn it back on only to debug.
think      = false
```

With no explicit `models.order`, the effective order is the detected shipped
agent CLI binaries first and `ollama` last. For example, a machine where two
supported CLIs are installed gets both before Ollama; a machine with neither
gets only Ollama as its model order. Keyword search is the final rescue after
that order, not a model provider. If every semantic route is unavailable, the
answer names each missing binary (for example, `claude binary not found in
PATH`) and the Ollama failure before using the rescue.

Detection affects only the absent-order case. `ROCA_MODELS_ORDER` still wins
over the file, and any explicit `order` in the file is preserved exactly.

### Where a value can be written

Precedence depends on the setting. `ROCA_MODELS_ORDER` overrides
`models.order`. Codex and Ollama's supported environment overrides win over
their `[models.<provider>]` table, then the compatible loose keys under
`[defaults]` (`model`, `ollama_model`, `ollama_base_url`, `codex_model`), then
the built-in default.

Local-binary `command`, `response_format`, `timeout_seconds`, and custom
substitution values have no environment override. Their provider table wins
over shipped preset data; an omitted custom-command timeout uses the 120-second
adapter default. When `[models].timeout_ms` or `probe_ms` is set, that shared
cascade budget takes precedence over the command timeout for the corresponding
request or probe.

API credentials are the exception: a key stored by `roca login` takes precedence
over the provider table's `api_key` and its environment variable.

`ROCA_MODELS_ORDER` overrides the order from the environment; `ROCA_MODELS_ORDER=none`
turns the model off entirely. There is no environment override for
`interpret_order`: it is a decision about where your data goes, and it is
written down in the file.

### Splitting the two inferences

A query costs two model calls. The first turns the question into SQL and
receives the question and the schema. The second turns the rows that SQL
returned into prose, and it is the only one that ever sees your data.

`interpret_order` puts that second call on providers of its own:

```toml
[models]
order           = ["claude"]    # writes the SQL: sees the question and the schema
interpret_order = ["ollama"]    # reads the rows: they go here and nowhere else
```

With that written, the result rows never leave the machine while the question
still goes to a frontier model. The resolution rules are the main order's: the
first available provider serves, an unknown name is a warning that names
`models.interpret_order` and the file, and `none` there is not an off switch,
it just leaves the two inferences together.

When no interpretation provider is available the rows go to the provider that
wrote the SQL, and the answer says so instead of pretending otherwise:

```
route model
SQL · provider codex · model gpt-5.6-sol · 8.2 s
search · 12 ms
the interpretation provider was not available (ollama: Ollama does not answer
at localhost:11434): the rows were read by codex
answer · provider codex · model gpt-5.6-sol · 11.4 s
```

With the split working, the second provenance is a line of its own, and in
`--json` it is `interpretation_provider`, `interpretation_model` and
`interpretation_provider_note`:

```
route model
SQL · provider codex · model gpt-5.6-sol · 8.2 s
search · 12 ms
answer · provider ollama · model qwen3.5:4b · 4.7 s
```

`roca doctor` reports that decision the same way it reports the main one: every
declared interpretation provider with its verdict and its remedy, and the one
that is going to read the rows.

## The providers

| Name | Class | Credential | Default model |
|---|---|---|---|
| `codex` | local Codex CLI | existing Codex CLI session | `gpt-5.6-luna` |
| `claude` | local Claude Code CLI | existing Claude Code session | `sonnet` |
| `deepseek` | frontier by key | `roca login deepseek`, `api_key`, or `DEEPSEEK_API_KEY` | `deepseek-chat` |
| `zai` | frontier by key | `roca login zai`, `api_key`, `ZAI_API_KEY`, or `ROCA_GLM_API_KEY` | `glm-4.6` |
| `xai` | frontier by key | `roca login xai`, `api_key`, `XAI_API_KEY`, or `ROCA_GROK_API_KEY` | `grok-4` |
| `ollama` | local floor | none | `qwen3.5:4b` |

The three by key are the **same adapter** with a different endpoint, model and
usual environment variable. A provider of your own is the same adapter again:
give it a table with a `base_url` and name it in the order. Every key provider
also answers to `ROCA_<PROVIDER>_API_KEY`; the two known by their model rather
than their vendor answer to the model's name too (`ROCA_GLM_API_KEY` for `zai`,
`ROCA_GROK_API_KEY` for `xai`), so an operator who thinks "GLM" or "Grok" never
has to learn the vendor's spelling.

```toml
[models]
order = ["mycorp", "ollama"]

[models.mycorp]
base_url    = "https://llm.internal/v1"
model       = "internal-7b"
api_key_env = "MYCORP_TOKEN"
```

Anything that speaks `POST {base_url}/chat/completions` works. No provider SDK
travels inside the binary: the HTTP adapters speak HTTP and JSON and nothing
else.

### Local-binary transport

A provider table can use a command instead of an HTTP `base_url`. The command
is an argv template, expanded without shell interpretation. `{prompt}` receives
the inference prompt; if it is absent, the prompt is sent on stdin. Every other
placeholder names a scalar in that same provider table: `{model}`, `{effort}`,
`{thinking}`, or any knob the CLI supports. Unknown placeholders are a
configuration error that names the provider, key, and file. Literal flags need
no placeholder. Set `response_format = "json"` when stdout is an object whose
`result` field is the answer; otherwise stdout is treated as answer text.

```toml
[models]
order = ["my-local-cli", "ollama"]

[models.my-local-cli]
command = ["my-local-cli", "--model", "{model}", "--effort", "{effort}"]
model = "local-smart"
effort = "high"
timeout_seconds = 120
```

A custom provider declares one transport: `base_url` or `command`. Declaring
both is a configuration error that names both keys and the configuration file.
Built-in providers may omit both and use their built-in transport.
The generic command transport works in the SQL and interpretation cascades and
through `roca doctor`, `roca login <provider>`, and
`roca model set <provider> <model>`.

`claude` and `codex` are shipped command-preset entries, not special adapters.
Their command, model, and timeout are all overridden by the same provider table
an operator uses for any other CLI. When either binary is already installed and
signed in, no configuration is needed. An explicit table is only for overrides;
for example:

```toml
[models]
order = ["claude", "ollama"]
interpret_order = ["ollama"]

[models.claude]
model = "sonnet" # aliases and full Claude model IDs are both accepted
```

The built-in command is pinned to Claude Code's non-interactive, single-turn
mode. It uses `--safe-mode`, a strict empty MCP configuration, no tools, no
skills, no Chrome integration, and no session persistence. It runs under the
dedicated `runner/` directory in La Roca's data directory, away from repository
instructions. The ingest scan explicitly excludes any runtime transcript keyed
to that directory.

The Codex preset uses its non-interactive `exec` transport with the shipped
default model, a read-only sandbox, an ephemeral session, no repository or user
configuration, and no color. Its prompt arrives on stdin, and it runs in the
same dedicated runner directory.

To use an explicit Claude binary path, copy the built-in isolation contract into
the command template:

```toml
[models.claude]
command = [
  "/opt/claude/bin/claude", "-p",
  "--output-format", "json", "--model", "{model}",
  "--safe-mode", "--strict-mcp-config", "--mcp-config", '{"mcpServers":{}}',
  "--tools", "", "--disable-slash-commands",
  "--no-session-persistence", "--no-chrome",
]
model = "sonnet"
response_format = "json"
timeout_seconds = 120
```

When `response_format = "json"`, La Roca reads the answer from stdout's
`result` field; otherwise stdout is plain answer text. This declaration is
independent of command arguments, so a CLI may accept JSON input while returning
text. Non-zero exits, malformed JSON, missing binaries, and timeouts are ordinary
provider failures: they produce the same honest degraded query path as an HTTP
provider failure. `roca doctor` lists every detected shipped binary, identifies
the provider the factory order selected, says that no La Roca login is required,
and reports a binary-specific remedy for anything missing or unusable.

## Fallback login flows

Detected agent CLIs need no La Roca login. Bare `roca login` lists all supported
flows; invoking it for a local CLI is an optional verification of the binary,
existing vendor session, and selected model.
These flows are setup fallbacks for users without a usable local CLI, not a
step between installation and the first query.

```
roca login              # lists local CLI, subscription fallback, and key providers
roca login codex        # optionally verifies the existing Codex CLI session
roca login claude       # optionally verifies the existing Claude Code session
roca login xai          # stores the key, then presents the model picker
roca login xai --model grok-4-fast  # validates and probes this exact model
roca doctor             # says whether it is usable, never what is in it
roca logout xai         # forgets the stored key
```

The Codex HTTP/OAuth transport remains available as a configured fallback. Set
`models.codex.base_url` to the subscription endpoint and keep `codex` in an
explicit order; `roca login codex` then opens the browser flow. Its session
lands in `credentials/codex.json` with mode `0600` under a `0700` directory and
renews itself. This compatibility path is no longer the fresh-install default.

A key login stores the secret under `credentials/` in a `.key` file whose
provider name is URL-escaped, so even a custom name cannot escape that
directory. Config-file `api_key` and the provider's environment variable keep
working; a key stored by login takes precedence.

Local CLI verification stores no credential. The CLI owns the operator's
existing session; La Roca probes it through the binary, offers the model
choice, and never reads or copies that session.

During login, La Roca offers known model IDs with an arrow-key selection.
OpenAI-compatible providers and Ollama supply their live catalogues through
`/models` and `/api/tags`. Codex uses the public models.dev catalogue, then a
cached or embedded snapshot when the catalogue is unavailable. A fallback list
is labelled as possibly stale, and `roca update` refreshes its cache. A
local-binary provider offers its declared or shipped choices interactively and
also accepts an explicit model ID because many CLIs accept aliases and full IDs
without exposing a catalogue.

Catalogue membership proves only that a catalogue-backed model exists. Before
changing `config.toml`, La Roca sends one minimal real request with the candidate
provider and model; a local binary uses the CLI's existing session. Only a
successful response writes the ID; rejection prints the provider's own error and
leaves the model configuration unchanged. `--model <id>` uses this same probe
path for non-interactive login.

`roca model set <model-id>` validates and probes the first configured provider
without re-running login. The explicit
`roca model set <provider> <model-id>` form remains available when switching a
different configured provider. Both forms share the gate implemented in
`internal/distribution/cli/model_validation.go`.

```
roca model set gpt-5.6-sol
roca model set ollama qwen3.5:4b
```

The fallback vendor OAuth flow is fragile and changes with no notice. That risk
is taken with eyes open and the mitigation is in the shape: a readiness or probe
failure moves selection to the next provider or the local floor. After a
provider has been selected, a query-time failure is reported and goes directly
to keyword rescue instead of silently retrying another provider. It never takes
down the process.

## What happens on a query

1. A provider is chosen **by availability**, never by exception: each one is
   asked `Ready` in order and the first yes serves. The ones behind it are not
   asked anything.
2. The model generates SQL and that SQL **always** passes the two-halved gate.
   A model is not above the gate.
3. Whatever fails from there on degrades to the keyword rescue and says which of
   four things went wrong: `model_unavailable`, `model_error`, `invalid_sql`,
   `sql_execution_error`.

A provider in an explicit order that says it is available and then fails is
**not** silently retried with the next one. The factory order has one declared
exception: when a detected local CLI's first real request proves that its
session is unusable, La Roca records that failure and tries the next ready
factory provider. This uses the query itself instead of spending an extra model
inference on a separate account probe.

Every answer down this path declares who answered:

```
route model
SQL · provider ollama · model qwen3.5:4b · 12.7 s
search · 8 ms
```

and in `--json`, `sql_provider`, `sql_model`, `sql_inference_ms`, `execution_ms`
and a `providers` array with every provider tried and why each one did or did
not serve. A full answer also carries `interpretation_provider`,
`interpretation_model` and `interpretation_ms` even when the same provider did
both jobs.

Three fields answer three different questions, and they are kept apart on
purpose:

| Field | Answers |
|---|---|
| `sql_provider_note` | who was asked: the providers ahead of this one were not available |
| `message` | what came back: the state of this answer |
| `model_sql` | what the model wrote, whether or not it ran |

Writing one over another is how an answer came to say *"the configured provider
is not available"* while reporting that same provider as its `sql_provider`. And
`model_sql` survives the keyword rescue answering over it: without it, a model
that writes badly cannot be told from a rescue that fired for another reason.

## What the model is told

The schema the model receives and the rules it is given come from **one read of
the same DDL the gate prepares its validation database with**, minus the same
tables the gate hides. Everything the prompt asserts about the data is generated
from that read: the tables with their columns, **how the tables join** (from the
DDL's own `REFERENCES` clauses), and every rule that names a column.

That is not tidiness. Two defects came out of not having it, and both looked
like a weak model until the rejected SQL was read:

- A blanket "always filter with `supersedes IS NULL`" rule is invalid because
  that column exists in `memories` and nowhere else, so every question
  answered out of `sessions` came back rejected. Different questions, one
  identical error.
- The prompt listed tables and columns but never how they connect, so `what
  tools does the pi agent use the most` produced `SELECT ... FROM tool_uses
  WHERE source_agent = 'pi'`. `tool_uses` carries `session_id` and nothing about
  who ran it. With the join paths declared, the same question produces the
  correct `JOIN sessions` and answers.

One rule is about the dialect rather than the schema, and it is there for the
same reason: `datetime('last month')` parses, evaluates to NULL and makes every
comparison false. Valid SQL that can never match is worse than a rejection,
because nothing complains.

## One retry with the engine's verdict

When the gate rejects what the model wrote, the rejection buys **exactly one**
more attempt, carrying the rejected SQL and the engine's own reason back to the
model.

That is not a repair invented here: the verdict comes from the same SQLite that
would have run the query, and it is the one piece of information that fixes it.
Measured against `qwen3.5:4b`, the first SQL is often invalid in a way the engine
describes precisely, and the model corrects it immediately when shown. One retry
and no more: a model that cannot fix it with the error in front of it will not
fix it on the fifth try, and each try costs seconds.

A query that is valid at once costs one request. Only the ones that need it pay.

## Diagnosing

`roca doctor` prints the resolved order, which providers are available, which
one is going to answer and, for each one that is not available, the exact
command that fixes it. It reports that a credential is **present**, never its
value: there is no code path that prints one. It also re-lists open capability
proposals; the post-update review contract is documented under
[Update](lifecycle.md#update).
