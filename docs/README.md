# Documentation

Reading order, from operator to contributor:

1. [Model providers](models.md): provider order, login, the local floor, and
   how the two query inferences choose their models.
2. [Ingest sources](ingest.md): declare downloaded data exports and understand
   their incremental boundary.
3. [The MCP plug](mcp.md): the stdio server, its five tools, the TOON answer
   contract, and the supported integration targets.
4. [Install, update, and uninstall](lifecycle.md): the binary's life cycle,
   verification on update, and consent on uninstall.
5. [Operations](operations.md): operational logs, redaction, retention, and
   the read-only boundary.
6. [Architecture](architecture.md): the four internal domains and the import
   rule that keeps them honest.
7. [Releases](releases.md): how versions are cut and artefacts are built.

The [README](../README.md) is the front page; these pages carry the depth.
The [changelog](../CHANGELOG.md) is maintained by release automation, one
entry per version.
