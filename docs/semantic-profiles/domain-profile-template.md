# Domain semantic profile template

This template is an adopter-owned extension of La Roca's shared
semantic-first retrieval and evidence workflow. It defines domain policy; it
does not create another memory store and never outranks live source rows.

Copy this template into the project that owns the domain. Keep the concrete
source names, paths, authorities, and review rules in that project rather than
in La Roca's public repository.

## Scope

- Project or domain:
- Purpose of the investigation:
- Preferred layers or source kinds:

## Source and authority rules

1. Identify which source type is authoritative for each claim.
2. Preserve the stable source id, locator, text, date, and provenance.
3. Require external verification for current or sensitive claims.
4. Keep contradictory source rows visible; do not silently merge them.

## Finding types

Classify useful candidates as one or more of:

- observation;
- interpretation;
- contradiction;
- unresolved question;
- coverage limitation.

## Completion rules

State what evidence is required before a finding is accepted, what makes it
unresolved, and which next probe should be run. The profile may narrow or
organize the investigation, but it must not remove source locators, context
recovery, provenance, contradiction reporting, or the distinction between a
candidate and an accepted finding.
