# language: en

@surface
Feature: The surface is the product
  As an operator or an agent
  I want every command to be queryable, machine-readable and honest
  so that I can automate without guessing.

  Background:
    Given La Roca is installed and initialized

  @fast
  Scenario Outline: F10-01 Every core command answers in valid JSON
    When I run "<command> --json"
    Then the command exits with code 0
    And standard output is valid JSON and nothing else
    And the output contains no traceback

    Examples:
      | command                                        |
      | roca layers                                    |
      | roca health                                    |
      | roca status                                    |
      | roca doctor                                    |
      | roca schema status                             |
      | roca ingest --dry-run                          |
      | roca plugins list                              |
      | roca mcp status                                |
      | roca query 'cuantas memorias hay'     |

  @fast
  Scenario: F10-02 A command that does not exist names the ones that do
    When I run "roca comando-que-no-existe"
    Then the command exits with a code other than 0
    And the output lists the available commands
    And the output contains no traceback

  @fast
  Scenario: F10-03 No message to the operator names something that no longer exists
    When I run "roca doctor"
    And I run "roca status"
    And I run "roca --help"
    Then no output names a component this version does not have

  @fast
  Scenario: F10-04 Warnings name the key, the file and the remedy
    Given the configuration has a key this version does not understand
    When I run "roca doctor"
    Then the warning names the key
    And the warning names the file it is written in
    And the warning names the exact command that fixes it

  @fast
  Scenario: F10-05 The binary does not write outside its declared paths
    Given a clean HOME with no trace of Roca
    And I take a fingerprint of the whole HOME
    When I run the installer, "roca init", "roca ingest", "roca query" and "roca stop"
    Then every path created outside the initial fingerprint is declared as Roca's own
    And no created path is outside the HOME

  @fast
  Scenario: F10-06 No output contains a personal path or a private name
    When I run the complete set of diagnostic commands
    Then no output contains an organization or client name
    And no output contains a project name that does not come from the ingested data

  @fast
  Scenario: F10-07 Every command's help exists and is honest
    When I ask for the help of every available command
    Then every help exits with code 0
    And every help describes what the command does
    And every announced option really exists
