# Documentation

Reading order, from operator to contributor:

1. [Model providers](models.md): automatic agent CLI detection, provider order,
   the local floor, fallback login flows, and how the two query inferences
   choose their models.
2. [Ingest sources](ingest.md): declare downloaded data exports and understand
   their incremental boundary.
3. [Plugins](plugins.md): extend the CLI with Git-style neighbor executables
   and compose the stable machine surfaces.
4. [The MCP plug](mcp.md): the stdio server, its five tools, the TOON answer
   contract, and the supported integration targets.
5. [Install, update, and uninstall](lifecycle.md): the binary's life cycle,
   verification on update, and consent on uninstall.
6. [Operations](operations.md): operational logs, redaction, retention, and
   the read-only boundary.
7. [Architecture](architecture.md): the four internal domains and the import
   rule that keeps them honest.
8. [Releases](releases.md): how versions are cut and artefacts are built.

The [README](../README.md) is the front page; these pages carry the depth.
The [changelog](../CHANGELOG.md) is maintained by release automation, one
entry per version.
