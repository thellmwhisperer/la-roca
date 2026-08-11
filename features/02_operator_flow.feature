# language: en

@install @human-flow
Feature: The operator's real flow
  As an operator installing La Roca on their own machine
  I want the exact sequence I type to work end to end
  so that I do not discover on my machine what nobody tested.

  @acceptance
  Scenario: F02-01 One-line installation from the release channel
    Given a clean HOME with no trace of Roca
    And a valid credential for the release repository
    When I download the installer from the repository and pipe it to a shell
    Then the command exits with code 0
    And the output contains the path of the installed binary
    And the output contains the path of the link that was created
    And if the binaries directory is not on the PATH, the output warns about it

  @acceptance
  Scenario: F02-02 The operator's immediate verification
    Given La Roca has just been installed from the release channel
    When I run "roca --version"
    Then the command exits with code 0
    And the output contains the installed version
    When I run "roca"
    Then the command exits with code 0
    And the output lists every available command
    And the output contains no traceback
    When I run "roca doctor"
    Then the command exits with code 0
    And every check appears with its verdict
    And every check whose verdict is not correct names its exact remedy

  @acceptance
  Scenario: F02-03 Bootstrap and a second doctor pass
    Given La Roca has just been installed from the release channel
    When I run "roca init"
    Then the command exits with code 0
    When I run "roca doctor"
    Then the command exits with code 0
    And no config, database or model check is in an error state

  @acceptance
  Scenario Outline: F02-04 Installing La Roca into an agent's config
    Given La Roca is installed and initialized
    And the agent "<agent>" has its configuration file with content of its own
    When I run "roca mcp install <agent>"
    Then the command exits with code 0
    And the configuration of "<agent>" contains an MCP server entry for Roca
    And all the previous content of that configuration is preserved byte for byte
    And a backup of the previous file exists

    Examples:
      | agent    |
      | codex    |
      | claude   |
      | opencode |

  @acceptance
  Scenario: F02-05 On teardown, the agent configs go back to how they were
    Given La Roca is installed and initialized
    And La Roca is installed in the configurations of "codex", "claude" and "opencode"
    When I run "roca uninstall --purge --json"
    Then the command exits with code 0
    And no agent configuration contains a Roca entry any more
    And every agent configuration keeps the rest of its content byte for byte
    And no agent configuration file has been deleted

  @fast
  Scenario: F02-06 A command the operator believes exists tells them the right one
    Given La Roca is installed and initialized
    When I run "roca serve"
    Then the command exits with a code other than 0
    And the output names the real command that does that
    And the output contains no traceback

  @fast
  Scenario: F02-07 Updating is part of the flow and has an answer
    Given La Roca is installed at an earlier release version
    When I run "roca update"
    Then the command exits with code 0
    And "roca --version" reports the new version
    And the previous database and configuration are still intact
    And the MCP entries in the agent configurations still point at a binary that exists

  @fast
  Scenario: F02-08 Updating refuses to overwrite a build that is not a release
    Given La Roca is installed at an earlier development build
    When I run "roca update"
    Then the command exits with code 1
    And the output names what is published and how to install it
    And the previous database and configuration are still intact

  @acceptance @slow
  Scenario: F02-09 Synthetic ingest of every supported source
    Given La Roca is installed and initialized
    And a HOME with the seeded world "operator"
    When I run "roca ingest --json"
    Then the command exits with code 0
    And the JSON output has "errors" equal to "0"
    And the JSON output reports a count for every seeded source
    When I run "roca ingest --json" a second time
    Then the command exits with code 0
    And the delta of the second ingest is zero in every category

  @fast
  Scenario: F02-10 A dry-run ingest writes nothing and can still count it
    Given La Roca is installed and initialized
    And a HOME with the seeded world "operator"
    When I run "roca ingest --dry-run --json"
    Then the command exits with code 0
    And the output is valid JSON
    And the JSON output has "dry_run" equal to "true"
    And the output contains no traceback
    And the database has not changed

  @acceptance
  Scenario: F02-11 Interactive uninstall with the operator's answer
    Given La Roca is installed and initialized with data
    When I run "roca uninstall" and answer "n" to the question about keeping data
    Then the command exits with code 0
    And the output contains "purged: yes"
    And no Roca artefact is left in the HOME
