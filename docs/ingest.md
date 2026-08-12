# Ingest sources

First-time path: [install, detect an already signed-in agent CLI, and query
without a La Roca login](lifecycle.md#install).

`roca ingest` reads agent stores from their configured platform locations and
reads downloaded account exports only when you name them. It fingerprints each
source file by path and content, so an unchanged rerun is a zero delta and a
newer export contributes only message identities that have not already landed.

## Declare an Anthropic data export

Request the official export from Claude web or Desktop under **Settings →
Privacy → Export data**, download it, and extract the zip. Anthropic documents
the export action in its
[data export guide](https://support.claude.com/en/articles/9450526-export-your-claude-data).

Add the extracted directory to the configuration file in the selected data
directory (normally `~/.roca/config.toml`):

```toml
[defaults]
anthropic_export_paths = [
  "~/exports/claude-data",
]
```

Then run:

```sh
roca ingest
```

Point to the extracted directory, not to an individual JSON file. La Roca reads
`conversations.json` and `memories.json` when present, and multiple directories
may be listed. It never scans Downloads or another broad directory for exports
and ignores `projects/`, `design_chats/`, `users.json`, and
`login_history.json`.

## Declare an OpenAI data export

Request the official export from ChatGPT under **Settings → Data Controls →
Export Data**, download it, and extract the zip. Add the extracted directory to
the configuration file in the selected data directory:

```toml
[defaults]
openai_export_paths = [
  "~/exports/chatgpt-data",
]
```

Then run `roca ingest`. Point to the extracted directory, not to
`conversations.json`. Multiple export directories may be listed; a later export
of the same account contributes only conversations and messages whose source
identities have not already landed.

Each `conversation_id` becomes an unprojected `chatgpt-web` session. La Roca
walks the `mapping` parent/children tree and pairs user messages with assistant
replies, retaining alternate branches. A node that cannot be read is discarded
on its own; readable descendants are reparented to its nearest surviving
ancestor. System, tool, empty, and hidden nodes are excluded by design rather
than reported as malformed.

The assistant message's `metadata.model_slug` supplies the model when present,
falling back to the conversation's `default_model_slug`; the provider is
`openai`. The export carries no token or cost counts, so those provenance
columns remain NULL. Epoch timestamps are normalized into the corpus's UTC ISO
8601 format.

`shared_conversations.json` and attachment files are counted in the ingest
summary as out-of-scope exclusions. `chat.html` is another rendering of the
conversation history and is ignored. La Roca does not open attachment bytes.

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
content agrees. Every unresolved collision is left untouched and reported as a
discard, so nothing that already landed is rewritten and no exchange is written
twice.

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
