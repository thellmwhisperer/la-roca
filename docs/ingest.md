# Ingest sources

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
separate exchanges instead of collapsing a branch. Human messages without a
reply and messages whose parent is missing are discarded, and `roca ingest`
reports each discard with its source record and reason.

Attachment and file names are retained as per-exchange metadata. La Roca does
not open their bytes. Entries from `memories.json` enter the `user` layer with
origin `cron` and source `claude-web`.
