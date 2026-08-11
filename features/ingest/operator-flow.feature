# language: en

@journey @install @human-flow
Feature: The operator's real flow
  As an operator installing La Roca on their own machine
  I want the exact sequence I type to work end to end
  so that I do not discover on my machine what nobody tested.

  @acceptance @slow
  Scenario: Synthetic ingest of every supported source
    Given La Roca is installed and initialized
    And a HOME with the seeded world "operator"
    When I run "roca ingest --json"
    Then the command exits with code 0
    And the JSON output has "errors" equal to "0"
    And the JSON output reports a count for every seeded source
    When I run "roca ingest --json" a second time
    Then the command exits with code 0
    And the delta of the second ingest is zero in every category
