# language: en

@acceptance @ingest
Feature: Cheap to repeat
  Fingerprints avoid needless reads and record identities keep growing sessions
  additive and idempotent.

  Scenario: A file whose fingerprint is unchanged is not even opened
    Given a Claude session has already been ingested
    And the unchanged session file can no longer be opened
    When I run ingest again
    Then the file is skipped by fingerprint without an error

  Scenario: Running ingest twice in a row changes nothing
    Given a Claude session is ready to ingest
    When I run ingest twice in a row
    Then the second ingest has a zero delta
    And the normalized row counts are unchanged

  Scenario: Re-ingesting an updated session adds only the new exchanges
    Given a Claude session with one exchange has already been ingested
    When a second exchange is appended and I run ingest again
    Then exactly one exchange is added
    And no session, thinking block, tool call or memory is added
