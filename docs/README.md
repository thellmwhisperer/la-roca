# Documentation

Reading order, from operator to contributor:

1. [Install, update, and uninstall](lifecycle.md): install the binary, let La
   Roca detect an already signed-in agent CLI, and ask away with no La Roca
   login; then follow the verified update and consent-based uninstall flows.
2. [Model providers](models.md): automatic agent CLI detection, provider order,
   the local floor, CLI-owned authentication, and how the two query inferences
   choose their models.
3. [Ingest sources](ingest.md): import downloaded data exports once and understand
   their incremental boundary.
4. [Plugins](plugins.md): isolated SQLite data plugins and Git-style neighbor executables
   and compose the stable machine surfaces.
5. [The MCP plug](mcp.md): the stdio server, its six tools, the TOON answer
   contract, and the supported integration targets.
6. [Operations](operations.md): the audit log contract for every CLI and MCP
   call, the query failures `roca doctor` reports, redaction, retention, and
   the read-only boundary.
7. [Architecture](architecture.md): the four internal domains and the import
   rule that keeps them honest.
8. [Releases](releases.md): how versions are cut and artefacts are built.

The [README](../README.md) is the front page; these pages carry the depth.
The [changelog](../CHANGELOG.md) is maintained by release automation, one
entry per version.
