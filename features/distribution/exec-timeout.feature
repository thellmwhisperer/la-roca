# language: en

@journey @distribution
Feature: Exec time limit
  A SELECT that would scan for minutes is cancelled at the five-second bound
  so it cannot hold the database.

  Scenario: CLI exec cancels a runaway statement at five seconds
    Given La Roca is installed and initialized
    When I exec the SQL "WITH RECURSIVE costly(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM costly WHERE n < 100000000) SELECT sum(n) FROM costly"
    Then the command exits with a code other than 0
    And the output contains "the validated SQL exceeded the time limit after 5s"
    And the command finished within 6 seconds

  Scenario: MCP exec cancels a runaway statement at five seconds
    Given La Roca is installed and initialized
    When I open an MCP session over stdio against the binary
    And I call the exec tool with the SQL "WITH RECURSIVE costly(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM costly WHERE n < 100000000) SELECT sum(n) FROM costly"
    Then the response is a tool error
    And the readable response contains "the validated SQL exceeded the time limit after 5s"
