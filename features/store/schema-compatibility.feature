# language: en

@journey @regression
Feature: The campaign's eight defects do not come back
  As the person responsible for La Roca being installable
  I want every defect that cost a night to have its own named scenario
  so that its return is identified in the failure title and not in a log.

  @regression:D-4 @fast
  Scenario: D-4b Formatting noise in the schema never blocks
    Given a HOME with a database whose schema differs only in whitespace, comments and constraint order
    When I run "roca init --json"
    Then the command exits with code 0
    And the JSON output has "database" equal to "adopted"
