# language: en

@query
Feature: The natural-language query and its two routes
  As an agent or a human asking La Roca something
  I want a fast answer when the question is known and a good answer when it is not
  so that I do not pay the model's price on questions that do not need it.

  Background:
    Given La Roca is installed and initialized
    And a HOME with the seeded world "golden-corpus"

  @fast
  Scenario: F04-01 A known question is answered by the fast route
    When I run "roca query 'cuantas memorias hay' --json"
    Then the command exits with code 0
    And the JSON output has "path" equal to "compiler"
    And the JSON output has "latency_ms" less than 250
    And the JSON output has "sql" not empty
    And the JSON output has "queryplan" not empty
    And no call has been made to the model provider

  @acceptance @slow
  Scenario: F04-02 A free question is answered by the model
    When I run "roca query 'que decisiones se tomaron sobre el formato del binario' --json"
    Then the command exits with code 0
    And the JSON output has "path" equal to "llm_fallback"
    And the JSON output has "sql" not empty
    And the JSON output does not have "degraded"
    And the JSON output has "latency_ms" less than 30000
    And the returned rows contain the sentinel seeded for that question

  @fast
  Scenario: F04-03 The reason the fast route declined travels with the answer
    When I run "roca query 'que decisiones se tomaron sobre el formato del binario' --json"
    Then the JSON output has "fallback_reason" not empty
    And that reason is one of the contract's declared reasons

  @fast
  Scenario: F04-04 The SQL can be asked for without running it
    When I run "roca query 'cuantas memorias hay' --sql-only --json"
    Then the command exits with code 0
    And the JSON output has "sql" not empty
    And the JSON output has no rows
    And the database has not been queried for data

  @fast
  Scenario: F04-05 The emitted SQL can be run as it is
    When I run "roca query 'cuantas memorias hay' --sql-only --json"
    And I run "roca exec" with the SQL it returned, in JSON format
    Then the command exits with code 0
    And the rows are equal to those of the direct query

  @fast
  Scenario: F04-06 Only reads are allowed
    When I run "roca exec 'DELETE FROM memories' --json"
    Then the command exits with a code other than 0
    And the output contains "Only SELECT statements are allowed"
    And the memory count has not changed

  @fast
  Scenario: F04-07 A chained statement does not slip through
    When I run "roca exec 'SELECT 1; DROP TABLE memories' --json"
    Then the command exits with a code other than 0
    And the memories table still exists

  @fast
  Scenario: F04-08 A question asking to mutate is refused with a reason
    When I run "roca query 'borra todas las memorias' --json"
    Then the command exits with a code other than 0
    And the output names the question as outside the scope of the query
    And no row has been returned
    And the memory count has not changed

  @fast
  Scenario: F04-09 An ambiguous question is named as ambiguous, not guessed
    When I run "roca query 'sesiones' --json"
    Then the command exits with a code other than 0
    And the output asks to be more specific
    And no row has been returned

  @fast
  Scenario: F04-10 Honest zero rows are not dressed up as an answer
    When I run "roca query 'que tiempo hace en madrid' --json"
    Then the command exits with code 0
    And the JSON output has zero rows
    And the JSON output declares the match was empty
    And the output contains no invented text

  @fast
  Scenario: F04-11 A free-text search finds what was seeded
    When I run "roca query 'guiones largos' --json --no-llm"
    Then the command exits with code 0
    And the JSON output has "path" equal to "compiler"
    And the first row contains the text seeded about guiones largos

  @fast @red-today
  Scenario: F04-12 A filtered question is never answered with the unfiltered aggregate
    When I run "roca query 'cuantas memorias mencionan Go' --json --no-llm"
    Then the answer is not the memory total
    And either the emitted SQL contains a filter by that term
    And or else the fast route declines and the question goes to the model

  @fast
  Scenario: F04-13 A layer constraint is always respected
    When I run "roca query 'ultimos feedback' --layer feedback --json --no-llm"
    Then the command exits with code 0
    And every returned row belongs to the layer "feedback"

  @fast
  Scenario: F04-14 Every answer says which version and which code it comes from
    When I run "roca query 'cuantas memorias hay' --json"
    Then the JSON output has "version" not empty
    And the JSON output has "source_sha" not empty

  @fast
  Scenario: F04-15 The truncation budget is respected
    Given there is a memory with content longer than 5000 characters
    When I run "roca query 'memoria muy larga' --max-chars 200 --json --no-llm"
    Then no text field of the answer exceeds 200 characters
    And the kept text includes the search match

  @fast
  Scenario: F04-16 A handoff memory is found by free-text search
    Given there is a handoff memory about "zingalor calabaza"
    When I run "roca query 'zingalor calabaza' --json --no-llm"
    Then the command exits with code 0
    And the first row contains the text "zingalor calabaza"
