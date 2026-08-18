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
4. [Local vector search](vector.md): the default-off `features.vector` switch,
   Windows and Unix Ollama setup, the one-time embedding-model download,
   hardware-conditioned indexing time, and the null-delta check.
5. [Agent parser contribution kit](agent-parsers.md): support another agent with
   a measured real store, a synthetic fixture, one parser file, and one registry
   line.
6. [Plugins](plugins.md): a copy-verbatim quickstart to your first installed
   plugin, then the manifest engine, isolated plugin-owned databases,
   executable capabilities, package lifecycle, and a build-your-own example.
7. [The MCP plug](mcp.md): the stdio server, its six tools, the TOON answer
   contract, and the supported integration targets.
8. [Operations](operations.md): memory-layer validation and repair, the audit
   log contract for every CLI and MCP call, the query failures `roca doctor`
   reports, the privacy-safe `roca doctor --report` support snapshot,
   redaction, retention, and the read-only boundary.
9. [Architecture](architecture.md): the database-neutral kernel, current domain
   map, query path, and internal import rule.
10. [Releases](releases.md): how versions are cut and artefacts are built.

The [README](../README.md) is the front page; these pages carry the depth.
The [changelog](../CHANGELOG.md) is maintained by release automation, one
entry per version.
