# language: en

@journey @install
Feature: The full installation cycle
  As an operator trying La Roca out on a machine of their own
  I want to install, use, stop and uninstall leaving no residue
  so that the machine is left as it was if I decide I do not want it.

  Background:
    Given a clean HOME with no trace of Roca

  @fast @acceptance
  Scenario: Installing bundles the core and optional vector executable with the domain stores
    When I run the installer for the current platform
    Then the command exits with code 0
    And the binaries directory contains only "roca" and "roca-vector"
    And the bundled resident plugin "roca-ops" is installed without an executable
    And the bundled resident plugin "roca-corpus" is installed without an executable
    And the bundled journey plugin "roca-cron" is installed without an executable
    And the bundled executable plugin "roca-vector" is installed with dormant state
    And the core file is a static binary with no third-party dynamic dependencies
    And there is no Python virtual environment in the HOME
    And there is no embedded interpreter in the HOME
    And running "roca --version" exits with code 0
    And the output of "roca --version" contains the version and the source SHA

  @fast
  Scenario: Querying works without anything having been started
    Given La Roca is installed and initialized
    And the runtime is not started
    When I run "roca query 'memories' --json"
    Then the command exits with code 0
    And the JSON output has "engines" not empty
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

  @fast @acceptance @plugin-staging
  Scenario: Plugin staging leftovers are swept on installation
    Given abandoned bundled plugin staging folders
    When I run "roca _install-bundled-plugins --json"
    Then the command exits with code 0
    And no bundled plugin staging folders remain

  @fast @acceptance @plugin-staging
  Scenario: Plugin staging stays empty after repeated installations
    When I run "roca _install-bundled-plugins --json"
    Then the command exits with code 0
    And no bundled plugin staging folders remain
    When I run "roca _install-bundled-plugins --json"
    Then the command exits with code 0
    And no bundled plugin staging folders remain

  @fast @acceptance @plugin-staging
  Scenario: Plugin staging held by another process is preserved without waiting
    Given a bundled plugin staging folder locked by the test process
    When I install bundled plugins while the staging lock is held
    Then the command exits with code 0
    And the held staging folder and lock file still exist
    When the test process releases the staging lock
    And I run "roca _install-bundled-plugins --json"
    Then the command exits with code 0
    And no bundled plugin staging folders remain

  @fast @acceptance @plugin-staging
  Scenario: Plugin staging cleanup preserves installed plugin data
    Given installed plugin databases and their sidecar files
    And abandoned bundled plugin staging folders
    When I run "roca _install-bundled-plugins --json"
    Then the command exits with code 0
    And no bundled plugin staging folders remain
    And every installed plugin directory and data file still exists

  @fast @acceptance @plugin-staging
  Scenario Outline: Plugin staging filesystem failures are reported safely
    Given the plugin staging filesystem fails at "<location>"
    When I run "roca _install-bundled-plugins --json"
    Then the command exits with a code other than 0

    Examples:
      | location       |
      | plugin root    |
      | lock file      |
      | stage creation |
