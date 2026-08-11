Feature: Honest queries without a model
  A missing model can degrade to literal rows, but it never becomes a false success.

  Background:
    Given an initialized home with no model
    And the memory "provider acceptance sentinel" exists

  Scenario: A question with no model still answers rows via literal search, and the exit code tells the truth
    Given no configured provider is available
    When I ask "provider acceptance sentinel"
    Then the command exits with code 1
    And the result used the literal search path
    And one row contains "provider acceptance sentinel"

  Scenario: Only SELECT ever reaches the database; the gate blocks everything else
    When I submit these statements to the SQL gate:
      | statement                         |
      | SELECT COUNT(*) FROM memories     |
      | DELETE FROM memories              |
    Then one statement is accepted and one is blocked
    And the database still contains 1 memory

  Scenario: A SELECT without LIMIT comes back with one
    When I submit the SQL "SELECT id FROM memories"
    Then the command exits with code 0
    And the returned SQL contains "LIMIT 1000"

  Scenario: --sql-only prints the SQL and touches nothing
    Given no configured provider is available
    When I ask only for SQL for "provider acceptance sentinel"
    Then the command exits with code 1
    And the returned SQL starts with SELECT
    And no rows were returned
    And the database still contains 1 memory

  Scenario: Zero rows is an honest zero, with the rescue tried first
    Given no configured provider is available
    When I ask "words absent from every memory"
    Then the command exits with code 1
    And zero rows are reported
    And the literal rescue is reported

  Scenario: Model SQL passes the gate, executes, and returns its rows
    Given the provider configuration is:
      | provider | model            | availability |
      | ollama   | gate-acceptance  | ready         |
    And the model answers with SQL "SELECT id, content AS text FROM memories ORDER BY id LIMIT 1"
    When I ask "provider acceptance sentinel"
    Then the command exits with code 0
    And the result used the model SQL path
    And exactly 1 row is reported
    And one row contains "provider acceptance sentinel"

  Scenario: A provider backed by a local binary answers the query
    Given a provider backed by a local binary answers with SQL "SELECT id, content AS text FROM memories ORDER BY id LIMIT 1"
    When I ask "provider acceptance sentinel"
    Then the command exits with code 0
    And the result used the model SQL path
    And exactly 1 row is reported
    And one row contains "provider acceptance sentinel"

  Scenario: Model SQL rejected by the gate never executes
    Given the provider configuration is:
      | provider | model            | availability |
      | ollama   | gate-acceptance  | ready         |
    And the model answers with SQL "DELETE FROM memories"
    When I ask "gate rejection absent terms"
    Then the command exits with code 1
    And the degraded reason is "invalid_sql"
    And zero rows are reported
    And the database still contains 1 memory
