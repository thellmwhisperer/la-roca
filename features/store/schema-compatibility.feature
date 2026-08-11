# language: en

@journey @regression
Feature: Equivalent database schemas remain adoptable

  @fast
  Scenario: Formatting noise in the schema never blocks
    Given a HOME with a database whose schema differs only in whitespace, comments and constraint order
    When I run "roca init --json"
    Then the command exits with code 0
    And the JSON output has "database" equal to "adopted"
