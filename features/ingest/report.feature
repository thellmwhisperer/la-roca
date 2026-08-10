# language: en

@acceptance @ingest
Feature: The honest summary
  The report accounts for work per source, totals every table delta, names every
  isolated failure and makes dry-run observably read-only.

  Scenario: Ingest ends with per-source counts and a total delta
    Given Claude and Codex artefacts are ready to ingest
    When I run ingest
    Then each seeded source has counts
    And the total delta equals the normalized rows added

  Scenario: Every skipped file and error is counted in the summary
    Given one unchanged session and one malformed session are ready to ingest
    When I run ingest
    Then the summary counts every skipped file and error detail

  Scenario: Every malformed record inside a session is counted
    Given a Claude session with 3 malformed records is ready to ingest
    When I run ingest
    Then the summary reports 3 record discards with reasons

  Scenario: Dry-run reports what it would read and writes nothing
    Given a Claude session is ready to ingest
    When I run ingest as a dry-run
    Then pending files are reported
    And the database is byte-for-byte unchanged
