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
rather than a turn, and the Claude web export states none of it.

Thinking text stays in `thinking_blocks`, keyed to its session and exchange; it
is not duplicated onto `exchanges`. Codex reasoning now lands there on the
exchange that produced it, alongside the other sources' thinking blocks.

The fingerprint of every versioned source includes its parser revision. When a
release teaches a parser to read more of a source, the next plain `roca ingest`
reopens the files it had already synced. A replay identifies an existing turn
within its session by its human and agent timestamps first, then by a fingerprint
of the human and agent text when timestamps are absent or ambiguous. It backfills
only fields that are still NULL after the content agrees; conflicting or
ambiguous anchors are left untouched and reported as discards, so nothing that
already landed is rewritten and no exchange is written twice.

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
