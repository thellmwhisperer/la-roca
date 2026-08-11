# Ingest sources

`roca ingest` reads agent stores from their configured platform locations and
reads downloaded account exports only when you name them. It fingerprints each
source file by path and content, so an unchanged rerun is a zero delta and a
newer export contributes only message identities that have not already landed.

## Declare an Anthropic data export

Request the official export from Claude web or Desktop under **Settings →
Privacy → Export data**, download it, and extract the zip. Anthropic documents
the export action in its
[data export guide](https://support.anthropic.com/en/articles/9450526-how-can-i-export-my-claude-data).

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

The path must be the directory containing `conversations.json` and
`memories.json`. Multiple directories may be listed. La Roca never scans
Downloads or another broad directory for exports, never opens attachment bytes,
and ignores `projects/`, `design_chats/`, `users.json`, and
`login_history.json`.
