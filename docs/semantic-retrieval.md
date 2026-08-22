# Semantic retrieval and evidence workflow

La Roca has two different jobs that must not be confused:

1. the core database and its auditable SQL/FTS surfaces preserve and retrieve
   authoritative rows;
2. an agent skill decides how to investigate a question, recover context,
   distinguish evidence from interpretation, and report uncertainty.

The optional `roca-vector` executable supplies semantic candidates for the
second job. It can combine the canonical corpus with explicitly selected data
plugins through the public `roca exec` boundary. Its install and trust boundary
are defined by the
[executable plugin contract](plugins.md#executable-only-packages), and its
setup is documented in the [roca-vector package guide](../plugins/vector/README.md).
When a candidate is returned, the agent must resolve the source text through
core before treating it as evidence; the vector index is not a replacement for
the core database.

The route is available only when the package is installed and both
`features.plugins = true` and `features.vector = true` are set. The plugins
switch is the master gate; the vector switch opts into this executable. Its
local embedding model must also be ready. If any prerequisite is missing, follow
[Degraded routes](#degraded-routes) instead of presenting a literal result as
semantic retrieval.

## Recommended route

For a conceptual question:

1. State the investigation purpose.
2. Run one bare semantic probe with `roca vector --json query "<bare concept>" 8`;
   add `--db-path /path/to/roca.db` after `vector` for a non-default core
   database.
   For a data plugin, add `--plugin <name>`; Biblioteca, for example, uses
   `--plugin biblioteca-conocimiento` and supports `--topic`, `--channel`,
   `--published-after`, and `--published-before`.
3. Inspect the returned source kind, stable source id, locator, and score as
   candidate retrieval metadata.
4. Resolve the live source rows with core, recover the available project, layer,
   date, and provenance fields, and inspect neighbouring context. Preserve a
   missing provenance value as “the source said nothing”.
5. Run deliberate radius probes for synonyms, adjacent concepts, entities,
   and relevant periods.
6. Deduplicate by stable source id without discarding distinct contexts.
7. Return a verdict with evidence, contradictions, coverage limits, and next
   probes.

The vector score is a ranking aid. It is not a confidence claim, and a result
without recovered source context is not evidence.

## Degraded routes

If the vector plugin or its local embedding model is unavailable, the agent
must say so. Literal FTS/SQL remains useful for explicit exact lookups and for
diagnostics, but it must not be silently presented as semantic retrieval for a
conceptual question.

This boundary keeps a missing model from producing a plausible but incomplete
answer and lets an operator choose whether to enable the local vector route.

## Domain skills

Projects may add a domain profile without changing the retrieval contract. A
profile can define:

- the project and layers to prefer;
- the source authority order;
- the domain's finding types and review states;
- the required evidence fields;
- the questions that count as complete or unresolved.

It may not remove source locators, context recovery, provenance, contradiction
reporting, or the distinction between a candidate and an accepted finding.

Data-plugin adapters follow the same rule: they store only embeddings,
fingerprints, and locators; the source text is resolved live. A plugin may
enrich the embedding input with metadata, but the live transcript content and
its hash remain the verification boundary.

The Libro de Economía's corpus skills are an example of this separation: the
shared workflow owns coverage and context recovery, while the project owns its
chapters, editorial categories, and verification rules.

The first concrete profile is [Libro de Economía](semantic-profiles/libro-economia.md).
Apply it after recovering live source context, not as a replacement for that
context. Future profiles, such as Satélites, should reuse this contract and
remain separate from the Vigilante Económico project.
