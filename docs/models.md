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
custom local command.

Experimental plugin support is an explicit, default-off feature:

```toml
[features]
plugins = true
```

This activates plugin discovery, read-only attachment, semantic routing, and
the `roca plugin` installer commands. See [Plugins](plugins.md) for the package
and trust contract. Leave the key absent or false for the release-default
behavior.

Managed agent-artifact refresh is independently default-off. The registry is
still updated and outdated installs are reported when this key is absent:

```toml
[features]
artifact_refresh = true
```

Enabling it lets `roca update` refresh unchanged SYSTEM zones while preserving
USER zones. See [Update](lifecycle.md#update) for legacy adoption, divergence,
backup, and force behavior.

An interactive `roca init` is the shortest way to create those overrides. It
starts with models rather than provider names: detected agent CLIs are grouped
as origins with their shipped default and a free-text option, while Ollama
contributes the models returned by its local `/api/tags` catalogue. After the
model choice, init auto-selects its harness when exactly one origin matches or
asks which harness to use when several match. The confirmed pair is probed when
it differs from the already-ready default, then `models.order` and
`models.<provider>.model` are edited in place. Existing files keep their
comments and unrelated settings and receive a named recovery backup when the
edit changes them. A backup created while retiring a credential-backed provider
is credential-redacted rather than byte-exact. The complete question and
automation contracts live in [Initialize](lifecycle.md#initialize).

Plain Enter through the chooser preserves the effective selection the normal
factory ordering would have made. Non-terminal init does not run this chooser
or write the model configuration; it reports the effective provider/model and
the configuration path once so automation stays question-free.

```toml
[models]
# The order they are tried in. The first available one serves.
order = ["codex", "claude", "ollama"]
# Optional. The order the RESULT ROWS are read by, when you want the two
# inferences on different providers. Leave it out and the provider that wrote
# the SQL also reads the rows, which is what every installation did before this
# key existed.
interpret_order = ["ollama"]
# Optional. Deep explore tries this stronger order first, then
# interpret_order, then order. Plain explore starts at interpret_order.
explore_order = ["claude"]
# Model budgets, in milliseconds. timeout_ms bounds a provider request;
# probe_ms bounds the availability question asked before every one.
timeout_ms = 90000
probe_ms   = 3000

[query]
# SQL that passed the read-only gate may execute for this long. The working
# default is 5000 ms when this key is absent. Set 0 to disable the bound. A
# value that is not a whole number of milliseconds, zero or more, is warned
# about and the default applies.
timeout_ms = 5000

[features]
# The two model-facing switches are enabled by default and each one is its own
# escape hatch. Every default-off experimental switch is documented with the
# subsystem it gates: plugins, roca_ops and cron in docs/plugins.md,
# artifact_refresh and release_redirects in docs/lifecycle.md. A switch that
# gates an independent plugin module is documented by that module itself.
# strict_input false skips the experimental prompt-attack signatures, for when
# an ordinary question is falsely rejected. ask_missing_referent false stops
# La Roca asking which project, release or provider a question left generic and
# lets the model answer the question as written. Anything that is not true or
# false is warned about and leaves the switch on.
strict_input         = true
ask_missing_referent = true

[models.codex]
model = "gpt-5.6-luna"

[models.claude]
# The built-in command uses Claude Code's existing signed-in session.
model = "sonnet"

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
`models.order`. Ollama's supported environment overrides win over its
`[models.ollama]` table, then the compatible loose keys under `[defaults]`
(`model`, `ollama_model`, `ollama_base_url`), then the built-in default. A
shipped CLI preset takes no environment override for its model: it reads
`models.<provider>.model`, then `defaults.<provider>_model` (for example
`codex_model`), then the shipped default.

Local-binary `command`, `models`, `response_format`, `timeout_seconds`, and
custom substitution values have no environment override. Their provider table
wins over shipped preset data, except `models`, which is added to the shipped
catalogue; an omitted custom-command timeout uses the 120-second adapter
default. When `[models].timeout_ms` or `probe_ms` is set, that shared
cascade budget takes precedence over the command timeout for the corresponding
request or probe.

`ROCA_MODELS_ORDER` overrides the order from the environment; `ROCA_MODELS_ORDER=none`
turns the model off entirely. There is no environment override for
`interpret_order` or `explore_order`: they are decisions about where your data
goes, and they are written down in the file.

### Splitting the two inferences

A full query normally costs two model calls. The first turns the question into
SQL and receives the question and the schema. The second turns the rows that SQL
returned into prose, and it is the only one that ever sees your data. A gate
rejection adds one correction call before the row-reading call; first-shot
successes and zero-result rescues do not pay for it.

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

### Investigation missions and deep routing

`roca explore` keeps the SQL role unchanged: the first inference sees the
question and schema, emits one SELECT, and never sees returned rows. At the same
`InterpretStream` call site used by `query --full`, an explicit explore context
selects a different interpreter mission:

- plain explore answers only what the rows support and adds short,
  single-concept trail hints;
- deep explore answers the rows, maps source counts, date clusters,
  co-occurring terms and negative space, then proposes two or three bare-word
  probes.

Terrain facts are calculated deterministically from the actual returned result
set before the second inference. They enter the prompt as fixed facts; the model
may phrase them but may not invent, extend, or recalculate them. The terrain is
computed locally over every returned row, and only its aggregates reach a
provider: the ten-row cap and the 240-character field budget still govern the
raw rows in the prompt, so an interpreter sees the capped row sample plus those
aggregates and never the full result set as text. Terrain terms come only from
cells a source stored as text, so a SQL NULL and a computed number contribute
nothing, and the provenance labels a query surface synthesizes about who wrote a
row (`author`, `agent`, `model`, `surface`, `provider`, `provenance`, and the
`source_` columns) are left out too, so the fleet's own naming never outranks the
corpus in a probe. Both CLI modes print the generated SQL as well as the prose
so the investigator learns the schema and can graduate to `query --sql-only`
plus `exec`.

Deep mode may use a stronger row-reading model without moving ordinary prose:

```toml
[models]
order           = ["codex"]
interpret_order = ["ollama"]
explore_order   = ["claude"]
```

Deep interpretation tries `explore_order`, falls back to `interpret_order`,
then to the main `order`; every unavailable fall is declared in the answer.
Plain explore starts with `interpret_order`. `roca doctor` diagnoses the deep
order separately and names its first available provider.

### The prose arrives complete

An interpretation provider may deliver its answer as a stream, and La Roca uses
that stream as internal transport only. The prose you see is always held until
the response is complete and the guardian has checked it against the rows, and
it is then published in one piece. That is deliberate: the guardian needs whole
sentences before it can prove a comparison against the same subject, the same
measured column and the same rows, and delete what those rows do not support.
Printing words as they arrive would put unproven prose on your terminal that no
later correction can take back, so the wait is the price of never showing a
claim the evidence has not proved. Buffered providers, `--json` and every
machine caller take the same path, and the answer is identical.

## The providers

| Name | Transport | Authentication | Default model |
|---|---|---|---|
| `codex` | local Codex CLI | owned by Codex CLI | `gpt-5.6-luna` |
| `claude` | local Claude Code CLI | owned by Claude Code | `sonnet` |
| `ollama` | local runtime | none | `qwen3.5:4b` |

La Roca stores no model-provider secrets. Additional providers must be local
command transports declared explicitly; remote HTTP providers are not part of
the runtime catalog.

### Local-binary transport

A provider table can declare a local command. The command is an argv template,
expanded without shell interpretation. `{prompt}` receives the inference prompt;
if it is absent, the prompt is sent on stdin. Every other placeholder names a
scalar in that same provider table: `{model}`, `{effort}`, `{thinking}`, or any
knob the CLI supports. Unknown placeholders are a configuration error that names
the provider, key, and file. Literal flags need no placeholder. Set
`response_format = "json"` when stdout is an object whose `result` field is the
answer; otherwise stdout is treated as answer text.

```toml
[models]
order = ["my-local-cli", "ollama"]

[models.my-local-cli]
command = ["my-local-cli", "--model", "{model}", "--effort", "{effort}"]
model = "local-smart"
models = ["local-smart", "local-fast"]
effort = "high"
timeout_seconds = 120
```

A custom provider declares `command`; built-in providers may omit it and use
their shipped command preset. `base_url` is supported only for local Ollama.
The generic command transport works in the SQL and interpretation cascades and
through `roca doctor`, `roca model check <provider>`, and
`roca model set <provider> <model>`.

A local CLI publishes no catalogue, so `models` is what `roca model set` may
choose from for that provider. Omit it and the only offer is the model already
configured, which makes the command a no-op for a custom provider. A shipped
preset is widened, never replaced: its aliases stay on offer beside whatever
the table declares.

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
model = "sonnet"           # aliases and full Claude model IDs both run
models = ["claude-opus-5"] # what `roca model set claude` offers beside the aliases
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
provider failures: they produce the same honest degraded query path as any
unavailable model. `roca doctor` lists every detected shipped binary, identifies
the provider the factory order selected and the `roca model check` that confirms
it, and reports a binary-specific remedy for anything missing or unusable.

## Authentication and model selection

Models authenticate through their own CLIs. La Roca stores no secrets and a
detected CLI needs no La Roca authentication command. Model checks and model
selection are separate operations:

```sh
roca model check          # probes the first configured provider
roca model check codex    # probes Codex without writing configuration
roca model check claude   # probes Claude without writing configuration
roca doctor               # diagnoses binaries, models, and remedies
```

`roca login` survives only as a hidden read-only alias for `roca model check`.
It takes no `--model` and writes nothing, so a script that used to select a model
with `roca login <provider> --model <id>` moves to
`roca model set <provider> <model-id>`.

`model check` sends one minimal real request through the configured provider and
never edits `config.toml` or provider order. When no provider is declared at all,
or the order is turned off with `ROCA_MODELS_ORDER=none`, it says so and succeeds:
an empty cascade is a configuration answer, not a failed probe. An order that
declares providers this build cannot use is the other empty cascade and gets its
own answer, with the warning that explains each drop; it is never reported as a
configuration that declared nothing. The two are told apart by the providers the
cascade actually dropped (`Cascade.Dropped`) and never by whether the
configuration produced a warning, because a retired key of a provider the order
never named warns without dropping anything. `roca models` and `model set` make
the same distinction from the same field. Under `--json`, an empty provider list
looks identical for both causes, so `roca models` carries the answer in a
`reason` field of its own, empty exactly when there is a catalogue to report;
`model check --json` reports it in the `reason` it already had. Both commands
always emit `warnings` as a list, empty rather than absent, so a script can
range over it without checking for the key. `model set` reads
the target provider's catalogue, refuses IDs outside it, and probes the selected
ID before writing only `models.<provider>.model`. A refused ID names the
catalogue it missed and how to widen it: declare it in `models.<provider>.models`
for a command transport, or pull it into Ollama first. The shared
catalogue-and-probe gate lives in
`internal/distribution/cli/model_validation.go`.

`roca model set <model-id>` validates and probes the first configured provider.
Both commands read "first" from the same live cascade, so `ROCA_MODELS_ORDER`
moves the check and the write together and neither acts on a provider the other
would skip. The explicit `roca model set <provider> <model-id>` form remains
available for another configured local command or Ollama. With no ID, an
interactive terminal offers the first provider's catalogue; pass only a provider
name to choose from that provider's catalogue.

```sh
roca model set
roca model set claude
roca model set gpt-5.6-luna
roca model set ollama qwen3.5:4b
```

Existing configuration that names a retired remote provider remains readable.
The provider is ignored with a warning and the rest of the cascade keeps
working. On first run, reconciliation offers to remove those retired settings
when the provider has a transport of its own, to replace it with a detected CLI,
or to drop it when no supported CLI is on `PATH`. A `command` you declared is a
transport of your own, so it is never removed, never migrated away, and keeps
answering while retired keys sit unread beside it. Declining changes nothing;
non-terminal runs emit one plain alert. A provider left with nothing but a
retired transport becomes available again only by accepting that proposal or by
removing the retired keys by hand, so declining deliberately keeps the
configuration unusable.

Old files under `~/.roca/credentials` are never read and never disable a
provider that works: they get a cleanup proposal of their own, and a Codex or
Claude CLI on `PATH` keeps answering whether or not you accept it. `roca init`
retires nothing behind its model confirmation; when the provider you choose
still carries retired settings or a leftover credential file, that proposal is
shown with its own yes or no first.

## What happens on a query

1. The question is checked before any model is called: it must contain text and
   stay within a deliberately generous 1000-character cap, the same on the CLI
   and over MCP. It is then read for the known jailbreak, role-hijack,
   delimiter and encoding shapes, and a match is refused with one generic
   message that never says which signature caught it. That message carries the
   only remedy that discloses nothing, `features.strict_input = false`, which
   skips those experimental signatures and keeps the text and length checks.
2. A question that names a referent without supplying it, as in *a specific
   project*, is answered with one clarifying question rather than a guess: the
   route is `ask`, no model is called, and `--json` carries
   `clarification_required` and the generic `missing_slot`. Setting
   `features.ask_missing_referent = false` lets the model answer such a
   question as written.
3. A provider is chosen **by availability**, never by exception: each one is
   asked `Ready` in order and the first yes serves. The ones behind it are not
   asked anything.
4. The model generates SQL, a repair step forgives known model-output mistakes,
   and the result **always** passes the two-halved gate. A model is not above
   the gate.
5. A model that answers `REFUSE` because the question is outside this database
   is taken at its word: the route is `refused`, the answer says so, and no
   keyword rescue answers over it. A refusal is a result, not a failure.
6. A gate rejection, or a failure when the validated statement runs, sends that
   SQL and the engine's exact verdict back to the same model once, through the
   same repair and gate path.
7. Whatever still fails from there on degrades to the keyword rescue and says which of
   five things went wrong: `model_unavailable`, `model_error`, `invalid_sql`,
   `sql_execution_error`, `sql_execution_timeout`. A statement killed by the
   `query.timeout_ms` bound is the last one, and it is never retried.

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
| `model_sql` | what the model wrote, untouched, whether or not it ran |

Writing one over another is how an answer came to say *"the configured provider
is not available"* while reporting that same provider as its `sql_provider`. And
`model_sql` survives the keyword rescue answering over it: without it, a model
that writes badly cannot be told from a rescue that fired for another reason.

### The repairs between the model and the gate

What the model wrote is repaired before the gate reads it, and only in ways
that are deterministic and named: a `<think>` block goes (`thinking_block`), a
single `sql` or bare Markdown fence is unwrapped (`code_fence`), prose before or
after the one `SELECT` is dropped (`surrounding_prose`), a repetition loop is
cut (`repetition_loop`), trailing semicolons are removed
(`trailing_semicolon`), and a top-level `UNION ALL` branch `ORDER BY`, with its
`LIMIT`, is taken out while the statement's final `ORDER BY` stays
(`union_order_by`). An implicit conjunction before a parenthesized FTS OR group
is made explicit (`fts_or_group`), because FTS5 rejects that otherwise valid
SQL shape at execution. The UNION repair has an aggressive fallback for shapes
the targeted pass cannot fix, and it is accepted only when what it produces
parses as one `SELECT`, so truncated output is left exactly as it came.

None of this authorizes execution: the gate then validates the repaired
statement with its unchanged rules, and what is still invalid degrades to
`invalid_sql` as before. `model_sql` keeps the untouched output and `repaired`
names every repair applied, in the JSON envelope and in the narration above the
rows, so the forgiveness is always auditable. SQL you wrote yourself and ran
with `roca exec` never goes through this step.

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

Nothing the model is given as data is given as instructions. Both prompts put
untrusted text in a section of its own, the question in the SQL prompt and the
question with the result rows in the interpretation prompt, and `&`, `<` and
`>` are entity-escaped inside it so no memory content can close that section
and speak as the prompt. The escaping is declared to the model in the same
breath, so it decodes the entities back to your own characters when it quotes
them and still never reads a decoded `<` as the start of a section. After the
untrusted text, each prompt repeats what it is allowed to return.

## One retry with the engine's verdict

When the gate rejects what the model wrote, or validated SQL fails at execution,
the failure buys **exactly one** more attempt, carrying the statement and the
engine's own reason back to the model.

That is not a repair invented here: the verdict comes from the same SQLite that
would have run the query, and it is the one piece of information that fixes it.
Measured against `qwen3.5:4b`, the first SQL is often invalid in a way the engine
describes precisely, and the model corrects it immediately when shown. One retry
and no more: a model that cannot fix it with the error in front of it will not
fix it on the fifth try, and each try costs seconds.

A query that is valid at once costs one request. Only the ones that need it pay.
Zero rows keep their existing literal rescue and never trigger a SQL retry;
`roca exec` SQL is user-authored and is never retried.

The envelope makes all outcomes distinguishable. `retried_sql` marks the
correction call; `retry_type` says `gate_rejection` or `execution_error`;
`first_model_sql`, `first_repaired`, and `retry_reason` retain the failed
attempt; `model_sql` and `repaired` describe the corrected attempt.
`sql_inference_ms` and `sql_provider_latency_ms` remain totals, while
`sql_retry_inference_ms` and `sql_retry_provider_latency_ms` attribute the retry
subset. A retry success has `retried_sql` without degradation, a second failure
has `invalid_sql` or `sql_execution_error` and falls through to the ordinary
rescue, and a rescue that fired for zero rows never asked for a correction of
its own.

The narration says the same thing above the rows: a query that paid for a
correction gets its own `SQL retry after gate rejection` or `SQL retry after
execution error` line with the time that correction took. Both audit streams
record the identical distinction, and what they keep is listed under
[Operations](operations.md#streams-and-contents).

## Diagnosing

`roca doctor` prints the resolved order, which providers are available, which
one is going to answer and, for each one that is not available, the exact
command that fixes it. It also states that agent models authenticate through
their own CLIs and that La Roca stores no secrets. Open capability proposals
are re-listed; the post-update review contract is documented under
[Update](lifecycle.md#update).
