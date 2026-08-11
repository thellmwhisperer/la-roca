# language: en

@journey @regression
Feature: Equivalent database schemas remain adoptable
  As the person responsible for La Roca being installable
  I want every defect that cost a night to have its own named scenario
  so that its return is identified in the failure title and not in a log.

  @fast @acceptance
  Scenario: A database identical column by column is adopted
    Given a HOME with an aged Roca database carrying tables from withdrawn features
    When I run "roca init --json"
    Then the command exits with code 0
    And the JSON output has "database" equal to "adopted"
    And the orphan tables are reported and do not block
    And the decision to adopt does not depend on the text of the create statements

  @fast
  Scenario: Formatting noise in the schema never blocks
    Given a HOME with a database whose schema differs only in whitespace, comments and constraint order
    When I run "roca init --json"
    Then the command exits with code 0
    And the JSON output has "database" equal to "adopted"
