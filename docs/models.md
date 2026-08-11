# Model providers

La Roca uses **frontier with a local floor**: the configured provider serves when there is a credential,
and with no network or no credential the fall to local Ollama is automatic. The
local one is the guaranteed floor, not the product's identity.

Questions use the first configured provider that reports itself ready. If the
selected provider is unavailable or produces unusable SQL, La Roca reports the
degraded state and attempts literal search over the local index.

## The configuration

One file, TOML, at `config.toml` inside the data directory (`~/.roca/`).
`roca doctor` prints the exact path it read.

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

With no file at all the order is `codex, ollama`: the subscription first, the
local floor last. The last element of the default order is always a provider
that can exist on any supported platform, so a machine that has nothing
configured still has a floor.

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
| `codex` | subscription (OAuth) | `roca login codex` | `gpt-5.6-luna` |
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

`claude` is a shipped command-preset entry, not a special adapter. Its command,
model, and timeout are all overridden by the same provider table an operator
uses for any other CLI. With Claude Code installed and already signed in, this
is enough:

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
provider failure. `roca doctor` runs the account probe and reports either the
working binary or the remedy: install Claude Code or put `claude` on `PATH`.

## Login

Same verb for every provider this build ships. Bare `roca login` lists them.

```
roca login              # lists subscription, local CLI, and key providers
roca login codex        # opens the browser, then presents the model picker
roca login claude       # verifies Claude Code's existing session; no browser flow
roca login xai          # stores the key, then presents the model picker
roca login xai --model grok-4-fast  # validates and probes this exact model
roca doctor             # says whether it is usable, never what is in it
roca logout codex       # forgets the subscription session
roca logout xai         # forgets the stored key
```

A subscription session lands in `credentials/codex.json` inside the data
directory, with mode `0600` in a directory with mode `0700`, and it renews
itself: you log in once. Before the browser opens, the command says what is
about to happen, what La Roca receives (an access token, never the password)
and how to revoke it. If the browser does not open, the address is printed and
pasting it works just as well.

A key login stores the secret under `credentials/` in a `.key` file whose
provider name is URL-escaped, so even a custom name cannot escape that
directory. Config-file `api_key` and the provider's environment variable keep
working; a key stored by login takes precedence.

Claude login stores no credential. Claude Code owns the operator's existing
session; La Roca verifies it through the local binary, offers the model choice,
and never reads or copies that session.

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

A vendor's OAuth flow is fragile and changes with no notice. That risk is taken
with eyes open and the mitigation is in the shape: when it breaks, the adapter
fails clearly and the cascade degrades to the next provider or to the local
floor. It never takes down a query.

## What happens on a query

1. A provider is chosen **by availability**, never by exception: each one is
   asked `Ready` in order and the first yes serves. The ones behind it are not
   asked anything.
2. The model generates SQL and that SQL **always** passes the two-halved gate.
   A model is not above the gate.
3. Whatever fails from there on degrades to the keyword rescue and says which of
   four things went wrong: `model_unavailable`, `model_error`, `invalid_sql`,
   `sql_execution_error`.

A provider that says it is available and then fails is **not** silently retried
with the next one. That is deliberate: retrying in silence turns "the frontier
provider is returning 500" into "the answers are odd today".

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
value: there is no code path that prints one.
