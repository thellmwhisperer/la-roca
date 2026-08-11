# language: en

@journey @install @human-flow
Feature: The operator's real flow
  As an operator installing La Roca on their own machine
  I want the exact sequence I type to work end to end
  so that I do not discover on my machine what nobody tested.

  @acceptance
  Scenario: One-line installation from the release channel
    Given a clean HOME with no trace of Roca
    And a valid credential for the release repository
    When I download the installer from the repository and pipe it to a shell
    Then the command exits with code 0
    And the output contains the path of the installed binary
    And the output contains the path of the link that was created
    And if the binaries directory is not on the PATH, the output warns about it

  @acceptance
  Scenario Outline: Installing La Roca into an agent's config
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

  @fast
  Scenario: Updating is part of the flow and has an answer
    Given La Roca is installed at an earlier release version
    When I run "roca update"
    Then the command exits with code 0
    And "roca --version" reports the new version
    And the previous database and configuration are still intact
    And the MCP entries in the agent configurations still point at a binary that exists

  @fast
  Scenario: Updating refuses to overwrite a build that is not a release
    Given La Roca is installed at an earlier version
    When I run "roca update"
    Then the command exits with code 1
    And the output names what is published and how to install it
    And the previous database and configuration are still intact
