# Libro de Economía semantic profile

This profile is a project-specific layer over La Roca's shared semantic-first
retrieval and evidence workflow. It does not create a second memory store and
it does not replace the live source rows or their locators.

## Scope

- Project: `Libro de Economía` within `Formador económico`.
- Primary purpose: recover material that can support a clear, traceable,
  non-generic educational book for readers without financial training.
- Preferred project material: the project's source manifest, evidence dossiers,
  micro-finding catalog, chapter/source matrix, QA records, and dated outputs.
- The manuscript is an editorial synthesis, not an authoritative source row.

## Source and authority rules

Use the source type that fits the claim, and preserve the exact source context:

1. A direct transcript, lesson, document, or synchronized capture is the
   authority for what that source actually said or showed.
2. CIC lessons are high-priority formative material for the topics they cover:
   Fundamental, Macroeconomía, and Momentum. They remain source material, not
   an automatic substitute for external verification.
3. `Trabajar desde casa`, `La Batalla por la economía mundial`, `Jon
   Economist`, `El arte de invertir`, and `David Battaglia` are authorized
   project sources whose contributions must be classified by claim and context.
4. Current or sensitive claims about markets, regulation, central banks,
   geopolitics, dates, figures, or named people require external verification
   before publication.
5. The project's manuscript, summaries, classifications, and AI-generated
   proposals are interpretations or navigation aids. They cannot outrank the
   underlying evidence.

When sources disagree, keep both source locators, state the contradiction, and
do not silently merge them into one fact.

## Finding types

Classify useful candidates as one or more of:

- `concept`: mechanism, definition, relationship, or explanatory idea;
- `scene`: concrete historical, personal, or economic example;
- `advice`: practical rule or decision aid, clearly attributed to its source;
- `pattern`: recurring behavior, incentive, or connection across sources;
- `indicator`: measurable datum or signal and its interpretation;
- `warning`: limitation, caveat, risk, or claim requiring verification;
- `contradiction`: materially incompatible claims or interpretations;
- `synthesis`: editorial connection supported by multiple located findings.

Do not treat a title, topic label, or whole video as a finding. The extraction
unit is the fragment plus enough surrounding context to understand it.

## Review and verification states

Keep coverage and editorial review separate:

- coverage: `pendiente` → `en_revision` → `revisado`;
- human review: `ready_for_human`, then accepted or returned with a reason;
- contextual verification: `pendiente_verificacion_contextual` until the
  surrounding passage and any linked capture have been checked;
- external verification: `pendiente_verificacion_externa` for sensitive or
  changing claims before publication.

`revisado` means the source was covered and checked; it does not by itself mean
that a finding is safe for the final manuscript. A rejected item must retain a
reason and its source locator when it is useful for auditability.

## Required evidence fields

An accepted or human-reviewable finding should retain, when available:

- stable La Roca source kind and source id;
- project, source name, source type, exact path, and source version or hash;
- fragment, timestamp or document location, plus preceding and following
  context;
- related capture or visual-evidence locator;
- finding type, candidate chapter(s), and intended editorial use;
- distinction between source observation, source interpretation, and editorial
  synthesis;
- contextual and external verification state;
- unresolved limitation, contradiction, or reason for exclusion.

If a field is absent, report that the source said nothing or that the evidence
was not available. Never fill a missing provenance field from a guess.

## Completion and unresolved criteria

A chapter-supporting evidence set is complete only after the relevant source
material has been covered sequentially, the context has been recovered,
duplicates and variants have been checked, chapter classification has been
reviewed, linked visual evidence has been addressed, and independent QA has
checked the result. A semantic hit alone is never complete.

Keep a result unresolved when its context is incomplete, its source cannot be
recovered, its provenance is missing for a claim that needs it, sources
contradict one another, or external verification is still required.

## Retrieval behavior

For a Libro question, apply the shared workflow and then this profile:

1. retrieve conceptual candidates semantically and keep their locators;
2. restrict interpretation to the Libro project and the relevant chapter or
   source layer when that information is known;
3. recover live text and neighboring context from La Roca's core;
4. classify each candidate using the finding types and states above;
5. report evidence, interpretation, contradictions, coverage limits, and the
   next probe separately;
6. keep source names, paths, and timestamps in the internal evidence record or
   HTML traceability annex, never in the reader-facing manuscript by default;
7. do not present the result as financial advice or as a guarantee of wealth.

Example conceptual probes include `how prices signal scarcity`, `what makes a
claim editorially traceable`, and `how macro conditions connect to investment
decisions`. The returned text still requires live context recovery and profile
classification before it becomes a finding.
