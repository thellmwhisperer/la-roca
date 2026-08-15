# Semantic retrieval and evidence workflow

La Roca has two different jobs that must not be confused:

1. the core database and its auditable SQL/FTS surfaces preserve and retrieve
   authoritative rows;
2. an agent skill decides how to investigate a question, recover context,
   distinguish evidence from interpretation, and report uncertainty.

The optional `roca-vector` executable supplies semantic candidates for the
second job. Its install, storage, and trust boundary are defined by the
[worked vector-plugin contract](plugins.md#worked-executable-example-vector-search).
When a candidate is returned, the agent must resolve the source text through
core before treating it as evidence; the vector index is not a replacement for
the core database.

## Recommended route

For a conceptual question:

1. State the investigation purpose.
2. Run one bare semantic probe with `roca vector --json query`; add
   `--db-path /path/to/roca.db` after `vector` for a non-default core database.
3. Inspect the returned source ids and scores as candidate locators.
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

The Libro de Economía's corpus skills are an example of this separation: the
shared workflow owns coverage and context recovery, while the project owns its
chapters, editorial categories, and verification rules.
