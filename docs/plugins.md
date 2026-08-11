# Plugins

La Roca plugins are ordinary neighbor executables. When `roca <name>` is not
a built-in command, La Roca looks for `roca-<name>` on `PATH` and hands control
to that executable. Arguments after the command, standard input,
standard output, standard error, and the exit status pass through unchanged.
Built-ins always win over a same-named plugin.

Resolution happens only when an unknown command is dispatched. There is no
startup scan, registry, manifest, or in-process extension API. The current
directory is never a plugin source, even when it appears on `PATH`. Run
`roca plugins` to list the `roca-*` executables available from the remaining
`PATH` directories and the path of each one. Add `--json` for a machine-shaped
catalogue.

## Building against the stable surfaces

A plugin should treat the `roca` process as its API and compose these public
surfaces:

- CLI commands with `--json` when it needs machine-shaped output.
- `roca query`, `roca exec`, and `roca sql` for reads; explicit SQL still goes
  through La Roca's read-only gate. Query inherits the detected-agent-CLI
  factory default, so a plugin does not introduce a separate login step.
- `roca store` for writes. Use the documented layers and pass
  `--origin plugin:<name>` so the plugin's records remain attributable and can
  be selected or purged by origin. Plugin names may contain letters, digits,
  hyphens, underscores, and dots.
- `roca mcp serve` when an MCP client is the more natural integration surface.

There is no sandbox. A plugin is an executable chosen from your `PATH` and
runs with your user account's permissions. Installing or invoking one carries
the same trust decision as running any other local program.
