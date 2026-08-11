# language: en

@regression
Feature: The campaign's eight defects do not come back
  As the person responsible for La Roca being installable
  I want every defect that cost a night to have its own named scenario
  so that its return is identified in the failure title and not in a log.

  @regression:D-4 @fast @acceptance
  Scenario: D-4 A database identical column by column is adopted
    Given a HOME with an aged Roca database carrying tables from withdrawn features
    When I run "roca init --json"
    Then the command exits with code 0
    And the JSON output has "database" equal to "adopted"
    And the orphan tables are reported and do not block
    And the decision to adopt does not depend on the text of the create statements

  @regression:D-4 @fast
  Scenario: D-4b Formatting noise in the schema never blocks
    Given a HOME with a database whose schema differs only in whitespace, comments and constraint order
    When I run "roca init --json"
    Then the command exits with code 0
    And the JSON output has "database" equal to "adopted"

  @regression:D-7 @fast @acceptance
  Scenario: D-7 The purge does not accuse itself of appearing late
    Given La Roca is installed and initialized with data
    When I run "roca uninstall --purge --json"
    Then the command exits with code 0
    And the JSON output has "purged" equal to "true"
    And the output does not contain "appeared after"
    And no Roca artefact is left in the HOME

  @regression:D-7 @fast
  Scenario: D-7b The purge still refuses to delete what it did not create
    Given La Roca is installed and initialized with data
    And a file Roca did not create exists in a directory of Roca's own
    When I run "roca uninstall --purge --json"
    Then the command exits with a code other than 0
    And that file still exists
    And the output names the path it refuses to delete and why

  @regression:D-8 @acceptance @slow
  Scenario: D-8 The three steps of a fresh machine come out clean
    Given a clean HOME with no trace of Roca
    When I run the installer for the current platform
    And I run "roca init --json"
    And I run "roca query 'how many memories are there' --json"
    And I run "roca uninstall --purge --json"
    Then the four commands exit with code 0
    And no output contains a traceback
    And no Roca artefact is left in the HOME
