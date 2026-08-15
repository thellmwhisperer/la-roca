# Ingest sources

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

`roca ingest` reads live agent stores from their platform locations. Pass one
extracted account-export directory to import that snapshot in the same run:

```sh
roca ingest /path/to/extracted-export
```

The path belongs only to that invocation. A later `roca ingest` with no path,
including the nightly run, reads only live Claude, Codex, Pi, OpenCode, Hermes,
and Cowork sources. It fingerprints each source file by path and content, so an
explicit rerun of the same export is a zero delta and a newer export contributes
only message identities that have not already landed.

The directory decides which vendor's parser reads it: `memories.json`, or a
`conversations.json` of `chat_messages` records, is a Claude export, and
`conversations.json` or `conversations-*.json` otherwise is a ChatGPT one. A
directory carrying neither is refused naming both layouts rather than attributed
to one of the two, as is a path that does not exist, is not a directory, or
cannot be read. Every refusal happens before ingest starts.

To add another agent source to the single binary, follow the public
[agent parser contribution kit](agent-parsers.md). Its conformance suite pins
detection, normalization, and destination routing with synthetic fixtures.

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
`conversations.json` and `memories.json` when present. Import another snapshot
with another explicit command. La Roca never scans Downloads or another broad
directory for exports and ignores `projects/`, `design_chats/`, `users.json`, and
`login_history.json`.

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

Each `conversation_id` becomes an unprojected `chatgpt-web` session. La Roca
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

Each conversation UUID becomes an unprojected `claude-web` session whose name
and summary are retained as metadata. Human and assistant messages are paired by
`parent_message_uuid`, then ordered by timestamp; alternate replies remain
separate exchanges instead of collapsing a branch. An unreadable message is
discarded on its own with its source record and precise reason; its readable
descendants are reparented to the nearest surviving ancestor, or begin a new
timestamp-ordered thread when none survives. A missing parent and an unpaired
readable message are not malformed records and do not poison later exchanges.

The conversation-file fingerprint includes the parser revision. After an ingest
fix changes normalization, the next `roca ingest` reopens an unchanged export,
backfills newly recoverable exchanges by message identity, and leaves sessions
and exchanges that already landed untouched. Later runs return to the normal
zero-delta fast path.

Attachment and file names are retained as per-exchange metadata. La Roca does
not open their bytes. Entries from `memories.json` enter the `user` layer with
origin `cron` and source `claude-web`.

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

## Per-exchange provenance

Every exchange carries what its own source recorded about how the answer was
produced: `model`, `provider`, `tokens_in`, `tokens_out`, `tokens_reasoning` and
`cost_usd`. They are filled from the artefact and from nothing else, so a column
is NULL wherever the source states nothing, which is normal and not missing
data. Read them with `IS NOT NULL` and never as a zero: a Claude transcript
counts tokens and names no provider, a Codex rollout counts the reasoning tokens
apart, Pi and OpenCode also price the turn, Hermes measures a whole session
rather than a turn, the Claude web export states none of it, and the ChatGPT
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

The default summary is one line per source with what it contributed, followed by
up to five reasons in each group for what was left out, with each reason
collapsed to a count.
The two groups are apart on purpose: `excluded` counts the records this build
never meant to read, which is most of a runtime log and is not a problem, and
`discards` counts records it could not read or safely match to an existing turn,
which is. `roca ingest
--verbose` adds the per-record detail with its absolute path for up to 100
retained records. Totals and the complete collapsed summary remain exact in JSON
output and the ingest log when a run leaves out more records than that; the log
retains the same bounded per-record detail.
