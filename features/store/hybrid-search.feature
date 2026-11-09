# language: en

@store
Feature: Hybrid search fuses a plugin row found by both legs
  `roca query` fuses full text and vectors. A row the corpus serves carries the
  same database label on both legs, so the two legs meet on it once.

  Scenario: A corpus row found by both legs comes back once with consensus
    Given a home made by roca init that ingested an agent session mentioning "harbor lantern"
    And vector search is switched on
    And a roca-vector on PATH answers query with that exchange as its only hit
    When I run 'roca query "harbor lantern" --databases corpus --json'
    Then the command exits with code 0
    And exactly one hit is that exchange from database "corpus" found by fts and vector with consensus

  Scenario: Require-both keeps a corpus row found by both legs
    Given a home made by roca init that ingested an agent session mentioning "harbor lantern"
    And vector search is switched on
    And a roca-vector on PATH answers query with that exchange as its only hit
    When I run 'roca query "harbor lantern" --databases corpus --require-both --json'
    Then the command exits with code 0
    And the hits include that exchange with consensus

  Scenario: Since filters retrieved hits by session date before printing
    Given a home made by roca init that ingested an agent session mentioning "harbor lantern"
    And vector search is switched on
    And a roca-vector on PATH answers query with that exchange as its only hit
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

  Scenario: Project filters fused hits to the named session project before printing
    Given a home made by roca init that ingested an agent session mentioning "harbor lantern"
    And vector search is switched on
    And a roca-vector on PATH answers query with that exchange as its only hit
    And agent sessions in projects alpha and beta mention orchard
    When I run 'roca query "orchard" --json'
    Then the command exits with code 0
    And the output contains "orchard alpha"
    And the output contains "orchard beta"
    And the search returns exactly 3 results
    When I run 'roca query "orchard" --project alpha --json'
    Then the command exits with code 0
    And the output contains "orchard alpha"
    And the output does not contain "orchard beta"
    And the output does not contain "the fixture exchange"
    And the search returns exactly 1 result
    When I run 'roca query "orchard" --project alpha'
    Then the command exits with code 0
    And the output contains "orchard alpha"
    And the output does not contain "orchard beta"
    And the output does not contain "the fixture exchange"
    When I run 'roca query "orchard" --project alpha --since 2026-10-21 --json'
    Then the command exits with code 0
    And the search returns no results
    When I run 'roca query "orchard" --project missing --json'
    Then the command exits with code 0
    And the search returns no results

  Scenario: Negative top is refused before searching
    Given a home made by roca init that ingested an agent session mentioning "orchard"
    When I run 'roca query "orchard" --top -5'
    Then the command exits with code 1
    And the output contains "--top must be zero or greater"
    And the output does not contain "rows["
    Given an MCP session has called roca_query "orchard" once
    Then the MCP answer is not an error and holds "orchard"
    When I run 'roca query "orchard" --top -5'
    Then the command exits with code 1
    And the output contains "--top must be zero or greater"
    And the output does not contain "rows["
    Given a clean HOME with a "corrupt" file at the database path
    When I run 'roca query "orchard" --top -5'
    Then the command exits with code 1
    And the output contains "--top must be zero or greater"
    And the output does not contain "not a database"

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

  Scenario: Three session hits offer one whole-session help line for the first exchange hit
    Given a home made by roca init that ingested an agent session mentioning "orchard"
    And agent sessions on October 1 and October 20 mention orchard
    When I run 'roca query "orchard"'
    Then the command exits with code 0
    And the output contains "rows[3]"
    And exactly one whole-session help line refers to the first exchange hit

  Scenario: A search hit offers a runnable SELECT for its whole session
    Given a home made by roca init that ingested an agent session mentioning "orchard"
    When I run 'roca query "orchard"'
    Then the command exits with code 0
    And the session help command reads the hit session in exchange order with max-chars 2000
    When I run the session help command verbatim
    Then the command exits with code 0
    And the output contains the three session exchanges in order
