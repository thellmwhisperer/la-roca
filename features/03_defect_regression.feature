# language: en

@regression
Feature: The campaign's eight defects do not come back
  As the person responsible for La Roca being installable
  I want every defect that cost a night to have its own named scenario
  so that its return is identified in the failure title and not in a log.

  @regression:D-2 @fast @acceptance
  Scenario: D-2 Startup on a virgin machine is green
    Given a clean HOME with no trace of Roca
    And La Roca is installed
    And the local model is available
    When I run "roca init --json"
    And I run "roca start --json"
    Then the command exits with code 0
    And the JSON output has "state" equal to "ready"
    And the JSON output has "problems" equal to "none"
    And the published MCP endpoint answers a handshake

  @regression:D-3 @fast
  Scenario: D-3 A startup failure names its real cause
    Given La Roca is installed and initialized
    And the runtime port is taken by a foreign process
    When I run "roca start --json"
    Then the command exits with a code other than 0
    And the JSON output has "problems" not empty
    And the failure detail names the concrete error type
    And the failure detail does not end in a generic count of sub-errors
    And the component's persistent log contains the full trace
    And the output to the operator contains no traceback

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

  @regression:D-5 @fast
  Scenario: D-5 An obsolete plugin name warns and does not kill startup
    Given La Roca is installed and initialized
    And the configuration declares the plugins "garden" and "media"
    And this version does not provide the plugin "garden"
    When I run "roca start --json"
    Then the command exits with code 0
    And the output contains a warning that names "garden"
    And that warning names the available plugins
    And that warning names the exact command to silence it
    And the JSON output has "problems" equal to "none"
    And the plugin "media" appears loaded

  @regression:D-5 @fast
  Scenario: D-5b A piece of the operator's data never becomes a code contract
    Given La Roca is installed and initialized
    And the configuration declares a plugin this version does not provide
    When I run "roca status --json"
    Then the command exits with code 0
    And the JSON output contains no runtime configuration error

  @regression:D-6 @fast @acceptance
  Scenario: D-6 stop leaves no orphans and does not hold the port
    Given La Roca is installed and initialized
    And the runtime is started
    When I run "roca stop --json"
    Then the command exits with code 0
    And the JSON output has "state" equal to "down"
    And no Roca process is left alive
    And the runtime port is free
    And a direct probe to that port is refused

  @regression:D-6 @fast
  Scenario: D-6b stop never signals a process it cannot prove is its own
    Given La Roca is installed and initialized
    And the runtime identity file points at a live foreign process
    When I run "roca stop --json"
    Then the command exits with a code other than 0
    And that foreign process is still alive
    And the output names the port holder without having touched it

  @regression:D-6 @fast
  Scenario: D-6c stop does not declare success while something answers on the port
    Given La Roca is installed and initialized
    And the runtime is started
    And a foreign process also answers on the runtime port
    When I run "roca stop --json"
    Then the command exits with a code other than 0
    And the JSON output does not have "state" equal to "down"

  @regression:D-7 @fast @acceptance
  Scenario: D-7 The purge does not accuse itself of appearing late
    Given La Roca is installed and initialized with data
    And the runtime is started
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
    And I run "roca start --json"
    And I run "roca query 'cuantas memorias hay' --json"
    And I run "roca stop --json"
    And I run "roca uninstall --purge --json"
    Then the six commands exit with code 0
    And no output contains a traceback
    And no Roca artefact is left in the HOME
