# language: en

@journey @install
Feature: The full installation cycle
  As an operator trying La Roca out on a machine of their own
  I want to install, use, stop and uninstall leaving no residue
  so that the machine is left as it was if I decide I do not want it.

  Background:
    Given a clean HOME with no trace of Roca

  @fast @acceptance
  Scenario: Installing bundles one binary and the resident operations store
    When I run the installer for the current platform
    Then the command exits with code 0
    And there is exactly one executable file "roca" in the binaries directory
    And the bundled resident plugin "roca-ops" is installed without an executable
    And the bundled journey plugin "roca-cron" is installed without an executable
    And that file is a static binary with no third-party dynamic dependencies
    And there is no Python virtual environment in the HOME
    And there is no embedded interpreter in the HOME
    And running "roca --version" exits with code 0
    And the output of "roca --version" contains the version and the source SHA

  @fast
  Scenario: Querying works without anything having been started
    Given La Roca is installed and initialized
    And the runtime is not started
    When I run "roca query 'how many memories are there' --json"
    Then the command exits with code 0
    And the JSON output has "path" not empty
    And no resident process has been started

  @fast @acceptance
  Scenario: A half installation converges on the next one
    Given La Roca is installed and initialized
    When I launch the installer of a new version and kill it with SIGKILL halfway
    Then the binary that was active still answers "roca --version"
    And the active version is still the previous complete one
    When I run the installer of that new version again and let it finish
    Then the command exits with code 0
    And "roca --version" reports the new version
    And no partial installation tree is left in the HOME

  @fast
  Scenario: A previous complete installation is recognized and not redone
    Given La Roca is installed at the target version
    When I run the installer of that same version
    Then the command exits with code 0
    And the output contains "already installed"
    And the active binary has not changed inode

  @fast
  Scenario: The installer never overwrites a file that is not its own
    Given a clean HOME with no trace of Roca
    And there is a regular file named "roca" in the binaries directory
    When I run the installer for the current platform
    Then the command exits with a code other than 0
    And the output names the file it refuses to overwrite
    And that file keeps its original content
