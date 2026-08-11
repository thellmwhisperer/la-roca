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

[models.deepseek]
api_key = "sk-..."          # or leave it out and export DEEPSEEK_API_KEY
model   = "deepseek-chat"

[models.ollama]
base_url   = "http://localhost:11434"
model      = "qwen3.5:4b"
keep_alive = "10m"
```

With no file at all the order is `codex, ollama`: the subscription first, the
local floor last. The last element of the default order is always a provider
that can exist on any supported platform, so a machine that has nothing
configured still has a floor.

### Where a value can be written

For each non-credential setting, in this order of precedence:

1. the environment,
2. its `[models.<provider>]` table,
3. a loose key under `[defaults]` (`model`, `ollama_model`,
   `ollama_base_url`, `codex_model`),
4. the built-in default.

API credentials are the exception: a key stored by `roca login` takes precedence
over the provider table's `api_key` and its environment variable.

`ROCA_MODELS_ORDER` overrides the order from the environment; `ROCA_MODELS_ORDER=none`
turns the model off entirely.

## The providers

| Name | Class | Credential | Default model |
|---|---|---|---|
| `codex` | subscription (OAuth) | `roca login codex` | `gpt-5.6-luna` |
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
travels inside the binary: the adapters speak HTTP and JSON and nothing else.

## Login

Same verb for every provider this build ships. Bare `roca login` lists them.

```
roca login              # lists subscription and key providers
roca login codex        # opens the browser, then presents the model picker
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

A key login stores the secret at `credentials/<provider>.key` with the same
permissions. Config-file `api_key` and the provider's environment variable keep
working; a key stored by login takes precedence.

After authentication, login lists canonical model IDs and accepts an arrow-key
selection; it never copies free text into the configuration. OpenAI-compatible
providers and Ollama supply their live catalogues through `/models` and
`/api/tags`. Codex uses the public models.dev catalogue, then a cached or
embedded snapshot when the catalogue is unavailable. A fallback list is
labelled as possibly stale, and `roca update` refreshes its cache.

Catalogue membership proves only that the model exists. Before changing
`config.toml`, La Roca sends one minimal real request with the newly stored
credential or subscription session. Only a successful response writes the
canonical ID; rejection prints the provider's own error and leaves the model
configuration unchanged. `--model <id>` uses this same validation path when a
non-interactive login needs an exact choice.

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

Claude stays out: its terms forbid it outside official tools.

## What happens on a query

1. A provider is chosen **by availability**, never by exception: each one is
   asked `Ready` in order and the first yes serves. The ones behind it are not
   asked anything.
2. The model generates SQL and that SQL **always** passes the two-halved gate.
   A model is not above the gate.
3. Whatever fails from there on degrades to the keyword rescue and says which of
   four things went wrong: `llm_unavailable`, `llm_error`, `invalid_sql`,
   `sql_execution_error`.

A provider that says it is available and then fails is **not** silently retried
with the next one. That is deliberate: retrying in silence turns "the frontier
provider is returning 500" into "the answers are odd today".

Every answer down this path declares who answered:

```
route llm_fallback · provider ollama · model qwen3.5:4b · 12762 ms
```

and in `--json`, `engine`, `model`, `llm_latency_ms` and a `providers` array with
every provider tried and why each one did or did not serve.

Three fields answer three different questions, and they are kept apart on
purpose:

| Field | Answers |
|---|---|
| `provider_note` | who was asked: the providers ahead of this one were not available |
| `message` | what came back: the state of this answer |
| `model_sql` | what the model wrote, whether or not it ran |

Writing one over another is how an answer came to say *"the configured provider
is not available"* while reporting that same provider as its `engine`. And
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
