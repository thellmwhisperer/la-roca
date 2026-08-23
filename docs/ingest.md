# Ingest sources

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

`roca ingest` incrementally reads supported local artefacts:

| Runtime | Artefacts |
|---|---|
| Claude Code | Sessions, subagent transcripts, and per-project memory files |
| Claude Desktop and Cowork | Session stores and Claude memory files |
| Claude web/Desktop export you point it at | Conversations, memories, projects, and design chats from the official Anthropic data export |
| ChatGPT export you point it at | Conversations from the official OpenAI data export |
| Codex | Sessions, memory, rule and skill files, and what matters from its state database |
| Qwen Code | Project chat sessions, including tool calls and source-recorded models |
| GLM | User skill documents and their supporting Markdown files |
| Cursor | Agent and legacy IDE sessions, prompts, thinking, and tool calls from its local SQLite stores |
| OpenCode | Sessions and exchanges, distilled from its local database |
| Pi | Complete session tree, including nested child runs |
| Hermes | Sessions and channel, usage and routing intel from its state database, plus curated MEMORY.md blocks |
| Grok Build | Sessions, from the session update stream and its metadata sidecar |
| Legacy store | A pre-federation `roca.db`: conversations into the corpus, memories into ops with their original layers |

Repository `AGENTS.md` and `CLAUDE.md` files are instructions and are never
ingested as memories. Live databases are read with SQLite `query_only` enabled
and a short busy timeout. Cursor databases are first serialized into an
immutable snapshot so active WAL content is included without changing Cursor's
stores.

Pass one extracted account-export directory to import that snapshot in the same
run:

```sh
roca ingest /path/to/extracted-export
```

The path belongs only to that invocation. A later `roca ingest` with no path,
including the nightly run, reads only live Claude, Codex, Qwen Code, GLM, Cursor,
Pi, OpenCode, Hermes, Grok Build, Cowork, and the pre-federation store. It
fingerprints each source file by path and content, so an explicit rerun of the
same export is a zero delta and a newer export contributes only message
identities that have not already landed. A live session file that grows
appends the new exchanges. It does not rewrite rows that already landed. A
genuine rewrite of an existing exchange records one digest-only lineage row,
never a second copy of the text. `roca compact` rewrites an older corpus
database onto that one-row law, empties archive bookkeeping that duplicated
current rows, and VACUUMs.

The directory decides which vendor's parser reads it: `memories.json`, or a
`conversations.json` of `chat_messages` records, is a Claude export, and
`conversations.json` or `conversations-*.json` otherwise is a ChatGPT one. A
directory carrying neither is refused naming both layouts rather than attributed
to one of the two, as is a path that does not exist, is not a directory, or
cannot be read. Every refusal happens before ingest starts.

To add another agent source to the single binary, follow the public
[agent parser contribution kit](agent-parsers.md). Its conformance suite pins
detection, normalization, and destination routing with synthetic fixtures.

Qwen Code project chats under `~/.qwen/projects` become sessions with complete
turns, tool calls, usage, and the model each assistant record states. Runtime
and debug records stay outside the corpus. GLM Markdown under `~/.glm/skills`
becomes user-layer memory, one stable record per file; GLM records no model for
those documents, so their model provenance remains empty.

Older configuration files may still contain `anthropic_export_paths` or
`openai_export_paths`. Those keys are leftovers: ingest ignores them, and they
can be removed.

## Import an Anthropic data export

Request the official export from Claude web or Desktop under **Settings →
Privacy → Export data**, download it, and extract the zip. Anthropic documents
the export action in its
[data export guide](https://support.claude.com/en/articles/9450526-export-your-claude-data).

Then pass the extracted directory directly:

```sh
roca ingest ~/exports/claude-data
```

Point to the extracted directory, not to an individual JSON file. La Roca reads
`conversations.json` and `memories.json` when present, plus each
`projects/<uuid>.json` entity and its `docs`, and each `design_chats/*.json`
record. Import another snapshot with another explicit command. La Roca never
scans Downloads or another broad directory for exports and ignores `users.json`
and `login_history.json`.

## Import an OpenAI data export

Request the official export from ChatGPT under **Settings → Data Controls →
Export Data**, download it, extract the zip, and run:

```sh
roca ingest ~/exports/chatgpt-data
```

Point to the extracted directory, not to an individual JSON file or the zip. La
Roca reads both the legacy `conversations.json` layout and the newer
`conversations-*.json` shards, and processes both when an export directory
contains both shapes. A later export of the same account contributes only
conversations and messages whose source identities have not already landed. When
legacy and sharded snapshots of the same conversation overlap, content
reconciliation lands no duplicate and keeps the provenance stated by whichever
snapshot recorded more about each answer; the legacy layout is the richer one,
and it wins whether both arrive in one run or months apart. An exchange an
earlier release stored carries no record of how much its snapshot stated, and an
absent record is not a low one: such a row is filled where it is empty and never
overwritten, so an upgrade cannot cost a corpus provenance it already had.

Each `conversation_id` becomes a `chatgpt-web` session. When the conversation
envelope has `gizmo_type` `snorlax` and a non-empty `gizmo_id`, that opaque
`g-p-...` id becomes the session project. The export does not carry the project's
display name, and La Roca does not invent one. `gizmo_type` `gpt` is a Custom
GPT: its gizmo fields stay in session metadata and do not become a project.
Ordinary chats without a snorlax gizmo stay unprojected. La Roca
walks the `mapping` parent/children tree and pairs user messages with assistant
replies, retaining alternate branches. An unreadable conversation envelope is
discarded on its own without stopping later conversations in the same file;
only structurally unreadable top-level JSON fails the file. A node that cannot
be read is also discarded on its own; readable descendants are reparented to
its nearest surviving ancestor. System, tool, empty, and hidden nodes are
excluded by design rather than reported as malformed.

The assistant message's `metadata.model_slug` supplies the model when present,
falling back to the conversation's `default_model_slug`; the provider is
`openai`. The export carries no token or cost counts, so those provenance
columns remain NULL. Epoch timestamps are normalized into the corpus's UTC ISO
8601 format. The per-message `update_time`, `status`, `end_turn`, `channel`,
`metadata.request_id`, and `metadata.turn_exchange_id` fields get no column of
their own; they are counted, because how much a snapshot stated about an answer
is what decides which of two snapshots of it keeps the provenance.

`shared_conversations.json`, `codex.json`, and attachment files are counted in
the ingest summary as out-of-scope exclusions and never warned about: Codex
conversations are a source of their own and not part of this reading.
`conversation_asset_file_names.json`, `chat.html`, and `ads.json` are expected
companions of an export and are ignored outright. La Roca does not open
attachment bytes.

## What enters the corpus

Each ordinary conversation UUID becomes an unprojected `claude-web` session
whose name and summary are retained as metadata. The official personal export
has no conversation-to-project join, so La Roca does not assign those sessions
to a project. Human and assistant messages are paired by
`parent_message_uuid`, then ordered by timestamp; alternate replies remain
separate exchanges instead of collapsing a branch. An unreadable message is
discarded on its own with its source record and precise reason; its readable
descendants are reparented to the nearest surviving ancestor, or begin a new
timestamp-ordered thread when none survives. A missing parent and an unpaired
readable message are not malformed records and do not poison later exchanges.

Each `projects/<uuid>.json` file becomes a named project entity (uuid, name,
description, prompt_template, timestamps) and one content row per `docs` item,
both on the `project` layer.
`memories.json` contributes `conversations_memory`, `project_memories` keyed by
project uuid, and each `memory_files` entry. A `design_chats/*.json` record
keeps the `project {uuid,name}` relation the export states; its session project
is that uuid. Membership is never inferred from titles or similarity.

The conversation-file fingerprint includes the parser revision. After an ingest
fix changes normalization, the next `roca ingest` reopens an unchanged export,
backfills newly recoverable exchanges by message identity, and leaves sessions
and exchanges that already landed untouched. Later runs return to the normal
zero-delta fast path.

Attachment and file names are retained as per-exchange metadata. La Roca does
not open their bytes. Entries from `memories.json` enter the `user` layer with
origin `cron` and source `claude-web`.

## Local memory files and completeness

Claude Code's durable memory content lives in the individual Markdown files
under `~/.claude/projects/*/memory/`. The neighbouring `MEMORY.md` is a
completeness manifest, not another memory: La Roca counts it as seen, refuses to
store the index as knowledge, and checks every relative Markdown link against
both disk and the corpus. A missing target or a target that did not land appears
as a coverage gap at the end of the run. Claude's own `~/.claude.json` project
map supplies the original working directory behind each encoded project folder,
so a normal ingest can attribute old memory rows whose project was previously
blank.

Codex's `raw_memories.md` is the primary aggregate and is split into its durable
thread records. `MEMORY.md` and `memory_summary.md` are downstream derived views;
they are counted and excluded rather than ingested as duplicate blobs. Ordinary
Markdown memory files remain primary content.

## Codex legacy history

The oldest Codex rollouts can contain only `session_meta`; their submitted
prompts survive separately in `~/.codex/history.jsonl`. Some of those rollouts
also kept prompt records of their own, in the same typeless shape, beside their
metadata. Those are recognized record by record inside the rollout, so the file
keeps both what its header states and the prompts it recovered, in whichever
order the older build wrote them. La Roca uses either set of history records as
a prompt-level fallback. Exact prompts close in time are reconciled with richer
rollout exchanges, regardless of which source lands first, and the richer
answer enriches the existing row instead of creating a duplicate. History
prompts with no matching rollout turn remain as prompt-only exchanges even when
the same session has other richer exchanges. A history record carrying a type is
a runtime log line rather than a prompt, so it is counted as an out-of-scope
exclusion instead of a malformed record. Malformed records are discarded
independently, and both groups are collapsed under stable history reasons in the
ingest summary.

When Codex's state database names a model or provider for the legacy session,
that provenance is retained on its recovered exchanges. The history format does
not record answers or per-exchange usage. Its session-wide `tokens_used` value
therefore remains session metadata, while the exchange's answer and token
columns remain NULL instead of receiving guessed values.

## Hermes database

Hermes keeps its conversations in `~/.hermes/state.db`, a SQLite database La
Roca opens read-only: a `query_only` connection that never writes and waits at
most a quarter second for the owner's lock before giving up. The private tree
defaults to `~/.hermes` (`hermes_home` or `HERMES_HOME`). Override only the
database with `hermes_db_path` or `HERMES_DB_PATH`. An absent Hermes home is a
clean no-op.

Every session is read, closed or not. Hermes writes `ended_at` only when a
session winds down cleanly, so sessions that were killed, abandoned, or run
through a channel that never closes them (acp, most TUI and CLI runs) carry
their messages and no ending. The end of such a session is its last recorded
message. A human turn with no recorded answer is kept with an empty answer once
the session is closed, and deferred while it is still open to be re-read on the
next run; in neither case is an answer invented.

Human and assistant text become exchanges, assistant reasoning becomes
thinking, and tool calls are paired with their results by the call id Hermes
records on both sides, each result carrying its call's summarized arguments and
its own error verdict. The session's `model` and `billing_provider` columns name
the provenance of every exchange; a missing model stays empty rather than
becoming a placeholder.

The live `sessions.source` channel (acp, cron, tui, cli, telegram, desktop)
is the session's surface qualifier: `source_surface` is `Hermes/<channel>`
when Hermes recorded one, and `Hermes` when it did not. The same channel is
kept in session metadata.

`session_model_usage` joins onto those sessions as operational `model_usage`
metadata (per-model provider and API base, requests, input, output, cache and
reasoning tokens, estimated and actual cost, pricing source, and first/last
observed times). `gateway_routing` joins by `session_key` or embedded
`session_id` as `routing` history. `system_prompts` join by
`system_prompt_hash` as session provenance (`hash`, a short preview, and byte
length), never as corpus content.

`~/.hermes/memories/MEMORY.md` is a mutable curated document, not an
append-only log. Each `§`-separated block is one memory whose identity is the
content hash. An unchanged rerun is a zero delta. A vanished block keeps its
row and is marked superseded; a new or edited block is a new memory. Layers
follow the block's nature (`user`, `feedback`, or `pattern`), origin is
`agent`, and `source_agent` is `hermes`. Per-block provenance preserves the
source file and block hash; the row's creation time records the ingest date.
The nine reserved hand-ingested rows (IDs 1152921504606847051 through
1152921504606847059) are also checked by exact content so those legacy
memories are not duplicated.

These Hermes stores are named exclusions, not corpus: `kanban.db`, the empty
`sessions.db`, `verification_evidence.db`, `USER.md` and other memory
companions, FTS shadow tables, and locks or leases. `projects.db` and
`cron/executions.db` are valid SQLite and can refuse a guest open (error 14)
while Hermes holds them; they hold no conversation content (empty project and
execution tables, with only discovery metadata), so they stay named exclusions
rather than Cursor-style snapshots.

## Pre-federation store

The previous generation kept harvested conversations and agent-written
memories in one SQLite file. `roca ingest` opens that `roca.db` with SQLite
`mode=ro`, `query_only`, and a short busy timeout. The default is the retired
product home beside `~/.roca`. Override the path with
`legacy_store_db_path` under `[defaults]`, or with `LEGACY_STORE_DB_PATH`. The
historical pre-federation aliases remain accepted for existing lab
configurations. An absent file is a clean no-op.

Sessions, exchanges, tool uses, and thinking blocks land in the corpus. The
source `session_id` is the dedupe key against sessions already federated; an
empty ID receives a deterministic fallback. Overlaps remain untouched and the
summary reports them as already present. An exact-payload match against a
session already in the federation (a different id, the same registration
payload) is the same overlap: it does not abort the source. Child-table counts
report only exchanges, thinking blocks, and tool uses actually inserted; an
overlapping child row that does not land is therefore absent from its inserted
count rather than reported by a separate overlap counter. Duplicate source
exchange numbers and thinking positions are disambiguated deterministically so
each distinct source row can land. `source_surface` is `Legacy store`, while
`source_agent` stays what the source stored. Tool rows whose source exchange
number is absent from that session are discarded when the file is read. That
count is source projection, not write-time overlap, and it is unchanged when
the session itself is later skipped as already present.

Memories land in ops and keep the layer, status, `created_at`, source
coordinates, and supersession relationship the source recorded: a handoff stays
a handoff. Expiry is not invented. Garden, proposal, run, and other
non-conversation tables are left out by design and reported with their collapsed
reasons. A second run over the same file adds nothing.

## Pi

Pi's private root defaults to `~/.pi` (`pi_root` or `PI_ROOT` can move it), and
its session store defaults to `agent/sessions/` below that root
(`pi_sessions_root` or `PI_SESSIONS_ROOT` can move just the sessions). La Roca
walks session JSONL recursively: ordinary sessions sit directly below their
encoded working-directory folder, while extension-created child runs can sit
several directories below a parent session. Legacy JSONL written directly below
`agent/` is also recognized. Symlinks are never followed.

Each version 3 session is a tree. La Roca projects the active branch, pairs user
and assistant messages into exchanges, and retains assistant text, thinking,
tool calls, tool verdicts, and tool parameters. A contextual custom message,
compaction summary, or branch summary participates in the conversation and is
stored as session-level context; a session-info entry supplies its title. A bash
execution that participates in a turn becomes a `bash` tool use. The terminal
assistant message supplies the exchange's model and provider exactly as Pi
recorded them, including provider-specific model identifiers, while usage and
cost are summed across the assistant messages that produced that answer.

The rest of the Pi root is inventoried but not opened as corpus. Prompts,
skills, global instruction files, extensions, settings, themes and provider
files are configuration. `missions/index/` contains runtime pointers and titles,
not the mission records they reference. `run-history.jsonl` and crash logs are
runtime history, while npm packages, cached repositories, binaries and caches
are installation state. Each family appears as a named exclusion in the ingest
coverage instead of being silently mistaken for thousands of conversations.
Pi currently exposes no separate durable memory store; the parser therefore
declares the corpus destination and invents no memories from configuration or
index metadata.

## Cursor

Current Cursor agent conversations live one session per
`~/.cursor/chats/<workspace-hash>/<session-uuid>/store.db`, with a sibling
`meta.json` supplying the title, working directory, and timestamps. La Roca
walks every Merkle list node in the database's blob store, not only the latest
root, so history retained behind compaction is not dropped. JSON role/content
messages become exchanges with assistant text, thinking, and named tool calls;
system prompts and injected context remain named exclusions.

These stores do not distinguish desktop from CLI origin: the measured `mode`
field is always `default`. Sessions therefore keep the existing canonical
`source_surface` value `Cursor`. They also keep Cursor's `cursor:<session-uuid>`
identity, so the shared writer deduplicates a session found through both the
agent and legacy IDE surfaces. The sibling metadata file participates in the
fingerprint, and a later plain `roca ingest` picks up changes without a manual
import step.

La Roca continues to read the legacy global
`~/Library/Application Support/Cursor/User/globalStorage/state.vscdb`, workspace
`state.vscdb` stores under `workspaceStorage/`, and
`~/.cursor/ai-tracking/ai-code-tracking.db`. The global IDE store's active
composer headers order user prompts and assistant bubbles into exchanges;
assistant text, thinking, named tools, timestamps, and the model and token counts
recorded on those bubbles enter the corpus. Workspace prompt and generation
arrays duplicate that history, and AI tracking rows describe code attribution
rather than conversations, so both remain reported as exclusions instead of
producing duplicate turns. Empty composers and bubbles outside the active
headers are likewise counted as exclusions.

Both Cursor database eras are opened read-only and serialized before parsing,
which includes committed WAL content without writing, checkpointing, or
otherwise changing Cursor's files. Every ingest summary therefore reports the
whole reading, including what was intentionally left out, and an unchanged
rerun is a zero delta.

## Grok Build

Grok Build keeps each session under `~/.grok/sessions/<encoded-cwd>/<session-id>/`,
filing the sessions of a working directory under that directory's URL-escaped
absolute path. Override the root with `grok_sessions_root` in the configuration
or the `GROK_SESSIONS_ROOT` environment variable. Each session directory carries
two read artefacts side by side: `summary.json`, the session's metadata, and
`updates.jsonl`, its durable protocol stream. The metadata is read first, as a
snapshot that names the session, its span, title, working directory, model and
git state; the update stream merges over it, so a summary without updates still
records the session and a re-read of either file updates only itself.

`session/update` records carry `user_message_chunk`, `agent_message_chunk`,
`agent_thought_chunk`, `tool_call`, `tool_call_update` and `plan` updates. User
chunks with one prompt index open one exchange; answer and thought chunks are
reassembled in record order, tool updates complete the tool use they name, and
the next prompt closes the exchange. Record timestamps anchor each side and the
session span, while the prompt's model identifies the answer. A tool whose
status reports a failure is stored as a failed tool use carrying its output, and
plan entries are kept as thinking beside the agent's reasoning. Parallel
`_x.ai/session/update` records are runtime machinery and are excluded by design,
as is a content block that is not text, such as an attachment on either side.

`events.jsonl` contains lifecycle telemetry rather than conversation content.
`compaction_requests/` and `recap_requests/` contain repeated snapshots derived
from the same update stream; they are not mined because they duplicate primary
turns rather than add turns absent from `updates.jsonl`. `chat_history.jsonl` is
also a compacted secondary view and is not the conversation source.
Files under `~/.grok/memtrace/` are process-memory telemetry (`start`, `sample`,
and `purge` records), not semantic memory or conversation. The coverage report
counts their files and records under that exclusion reason without ingesting
them.

## OpenCode

OpenCode's SQLite store is read as a snapshot through a query-only connection.
Every finished durable message becomes its own exchange: user text occupies the
human side, while each assistant message keeps its own text, model, provider,
usage and cost on the agent side. This message-level identity matters because a
single user parent can have several assistant children produced by different
models. An assistant message with no completion or with a running tool remains
deferred for the next ingest rather than landing half-written.

Assistant `reasoning` parts become queryable thinking blocks. Completed and
failed `tool` parts become structured tool uses with their recorded input and
error, and `patch` parts contribute their source-recorded hash and changed-file
list to searchable agent text. An assistant message whose only content is its
tools still lands with those tool names as its agent text, so a tool-only step
is not an invisible empty answer. The session's `todo` rows are retained as the
ordered `todos` list in session metadata. Step-start and step-finish parts are
reported as excluded runtime telemetry; the event table is never read.

The OpenCode parser revision is part of the database fingerprint. The first
plain ingest after this reading upgrade splits historical paired rows by source
message identity, removes assistant content formerly attached to the user row,
and adds the recovered assistant messages once. Later runs are zero-delta. The
summary prints messages seen, converted and skipped, with a count for each skip
reason, so coverage does not have to be inferred from exchange totals.

La Roca also reads the companion bot's installed-mode `bot-*.log` files from
`~/Library/Application Support/opencode-telegram-bot/logs` on macOS,
`%APPDATA%\opencode-telegram-bot\logs` on Windows, or
`${XDG_CONFIG_HOME:-~/.config}/opencode-telegram-bot/logs` on Linux. Override
that directory with `opencode_telegram_bot_logs` in the configuration or
`OPENCODE_TELEGRAM_BOT_LOGS` in the environment. Each
`id=ses_...` occurrence marks the matching OpenCode session with metadata
`channel=telegram`; its metadata also retains the bot log path and the date
recorded on that line. The canonical `source_surface` remains OpenCode. This
evidence is additive, so a session stays marked after its log rotates away.
An absent directory or a directory with no matching logs is a named coverage
exclusion and does not fail ingest.

## Per-exchange provenance

Every session also carries `source_surface`, the canonical harness known from
the detector and parser family that opened it (`Claude Code`, `Codex CLI`,
`OpenCode`, `Grok Build`, and so on). It is first-class provenance, not metadata
read from the artifact, except Hermes, whose live channel is the one dimension
that would otherwise collapse every session to `Hermes`. Harvested memories use
the existing `source_surface` column under the same rule and keep
`source_model` NULL unless their source actually recorded a model.

Every exchange carries what its own source recorded about how the answer was
produced: `model`, `provider`, `tokens_in`, `tokens_out`, `tokens_reasoning` and
`cost_usd`. They are filled from the artefact and from nothing else, so a column
is NULL wherever the source states nothing, which is normal and not missing
data. Read them with `IS NOT NULL` and never as a zero: a Claude transcript
counts tokens and names no provider, a Codex rollout counts the reasoning tokens
apart, Qwen Code names the model and counts each request in a tool loop, Cursor
names the model and usage only when its answer bubbles do, Pi and OpenCode also
price the turn, Hermes measures a whole session rather than a turn, the Claude
web export states none of it, and the ChatGPT
export names its model and provider without stating usage.

Thinking text stays in `thinking_blocks`, keyed to its session and exchange; it
is not duplicated onto `exchanges`. Codex reasoning now lands there on the
exchange that produced it, alongside the other sources' thinking blocks. When a
historical match has no exchange number, the schema has no key for replayed
thinking blocks, so they are left out and each one is reported as a discard.

The fingerprint of every versioned source includes its parser revision. When a
release teaches a parser to read more of a source, the next plain `roca ingest`
reopens the files it had already synced. A replay identifies an existing turn
within its session by its human and agent timestamps first, then by a fingerprint
of the human and agent text when timestamps are absent or ambiguous. Timestamp
anchors compare parsed RFC3339 instants, so equivalent UTC offsets and trailing
fractional zeroes match; this also works for historical rows with no exchange
number. String timestamps without an RFC3339 zone, or with another unsupported
spelling, are omitted at the parser boundary instead of being assigned a guessed
instant. In a same-instant collision, one numbered original may be selected over
compatible numberless duplicates; numbered peers and conflicting text remain
ambiguous. The replay backfills only fields that are still NULL after the
content agrees. The one exception is a source that measures how much each of its
snapshots stated about an answer, which today is the ChatGPT export: a reading
that measurably stated more than the one the row's provenance came from states
the provenance columns instead of only filling them, under the rules in
[Import an OpenAI data export](#import-an-openai-data-export). Every unresolved
collision is left untouched and reported as a discard, so an ambiguous match
rewrites nothing and no exchange is written twice.

## Reading the summary

Every run ends with one `coverage:` block. Its file totals close as `seen =
ingested + skipped`; `claimed` is the subset assigned to an ingest adapter, and
each skip class is named with its exact count. Claude manifest gaps appear in
the same block. When an OpenCode store is present, the block also compares its
session, message, part, todo, and lost-and-found row counts with the normalized
sessions, exchanges, thinking blocks, and tool uses currently in the corpus.
Paths are shown only by `roca ingest --verbose`.

The default summary also keeps one line per source with files seen, parsed,
skipped and excluded plus what it contributed, followed by up to five reasons in
each group for what was left out, with each reason collapsed to a count. A
source read from a live database rather than files also prints a `saw` line
beneath its row — the raw sessions and messages it observed before
normalization — so the converted counts read against the whole store and not
only what landed.
The two groups are apart on purpose: `excluded` counts the records this build
never meant to read, which is most of a runtime log and is not a problem, and
`discards` counts records it could not read or safely match to an existing turn,
which is. `roca ingest
--verbose` adds the per-record detail with its absolute path for up to 100
retained records. Totals and the complete collapsed summary remain exact in JSON
output and the ingest log when a run leaves out more records than that; the log
retains the same bounded per-record detail.

JSON output also carries `files_seen` and `file_coverage`. The latter groups
every file under `parsed`, `pending`, `skipped`, `excluded`, or `error` with a
reason and exact count. Those categories total `files_seen`, so an operator can
distinguish an unchanged content file from a runtime family La Roca deliberately
did not parse.

A file that parsed and then could not be written is isolated: the run names it
under `write failed`, continues with the rest of the corpus, and `roca ingest`
exits non-zero so a direct invocation and a cron ride agree. A file that could
not be read is still a counted, non-fatal skip. Patching session metadata never
writes a duplicate exact-payload row; the unique index stays, and the artefact
keeps the metadata it already had.
