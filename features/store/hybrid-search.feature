# language: en

@store
Feature: Hybrid search fuses a plugin row found by both legs
  `roca query` fuses full text and vectors. A row the corpus serves carries the
  same database label on both legs, so the two legs meet on it once.

  Background:
    Given a home made by roca init that ingested an agent session mentioning "harbor lantern"
    And vector search is switched on
    And a roca-vector on PATH answers query with that exchange as its only hit

  Scenario: A corpus row found by both legs comes back once with consensus
    When I run 'roca query "harbor lantern" --databases corpus --json'
    Then the command exits with code 0
    And exactly one hit is that exchange from database "corpus" found by fts and vector with consensus

  Scenario: Require-both keeps a corpus row found by both legs
    When I run 'roca query "harbor lantern" --databases corpus --require-both --json'
    Then the command exits with code 0
    And the hits include that exchange with consensus

  Scenario: Since filters retrieved hits by session date before printing
    Given agent sessions on October 1 and October 20 mention orchard
    When I run 'roca query "orchard" --json'
    Then the command exits with code 0
    And the output contains "orchard 2026-10-01"
    And the output contains "orchard 2026-10-20"
    And the search returns exactly 3 results
    When I run 'roca query "orchard" --since 2026-10-15 --json'
    Then the command exits with code 0
    And the output contains "orchard 2026-10-20"
    And the output does not contain "orchard 2026-10-01"
    And the output does not contain "the fixture exchange"
    And the search returns exactly 1 result
    When I run 'roca query "orchard" --since 2026-10-20'
    Then the command exits with code 0
    And the output contains "orchard 2026-10-20"
    And the output does not contain "orchard 2026-10-01"
    When I run 'roca query "orchard" --since 2026-10-21 --json'
    Then the command exits with code 0
    And the search returns no results

  Scenario Outline: Since accepts only a calendar date
    When I run 'roca query "orchard" --since=<date>'
    Then the command exits with code 1
    And the output contains "--since must be a date in YYYY-MM-DD format"

    Examples:
      | date       |
      | 2026-02-30 |
      | yesterday  |
      | 24h        |
      | ""         |
