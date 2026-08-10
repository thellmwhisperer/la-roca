# language: en

@install
Feature: The full installation cycle
  As an operator trying La Roca out on a machine of their own
  I want to install, use, stop and uninstall leaving no residue
  so that the machine is left as it was if I decide I do not want it.

  Background:
    Given a clean HOME with no trace of Roca

  @fast @acceptance
  Scenario: F01-01 Installing is copying one binary
    When I run the installer for the current platform
    Then the command exits with code 0
    And there is exactly one executable file "roca" in the binaries directory
    And that file is a static binary with no third-party dynamic dependencies
    And there is no Python virtual environment in the HOME
    And there is no embedded interpreter in the HOME
    And running "roca --version" exits with code 0
    And the output of "roca --version" contains the version and the source SHA

  @fast
  Scenario: F01-02 A virgin machine can say it is not initialized yet
    Given La Roca is installed but not initialized
    When I run "roca status"
    Then the command exits with a code other than 0
    And the output names the exact command "roca init" as the remedy
    And the output contains no traceback

  @fast @acceptance
  Scenario: F01-03 init creates config, database and model, and is idempotent
    Given La Roca is installed but not initialized
    And the local model is available
    When I run "roca init --json"
    Then the command exits with code 0
    And the JSON output has "config_path" pointing at a file that exists
    And the JSON output has "db_path" pointing at a file that exists
    And the JSON output has "model.ready" equal to "true"
    And the JSON output has "ingest.errors" equal to "0"
    When I run "roca init --json" a second time
    Then the command exits with code 0
    And the database has not changed path
    And no additional database has been created

  @fast
  Scenario: F01-04 The database init creates is the one every command reads
    Given La Roca is installed and initialized
    When I run "roca store --layer project --content 'ancla de aceptacion'"
    And I run "roca query 'cuantas memorias hay' --json"
    Then the JSON output has "rows[0].COUNT(*)" equal to "1"
    And no command has needed "--db-path" to find the database

  @fast @acceptance
  Scenario: F01-05 The runtime starts on demand and stops leaving no residue
    Given La Roca is installed and initialized
    When I run "roca start --json"
    Then the command exits with code 0
    And the JSON output has "state" equal to "ready"
    And the JSON output has "problems" equal to "none"
    And the published MCP endpoint answers a handshake
    When I run "roca status --json"
    Then the JSON output has "state" equal to "ready"
    When I run "roca stop --json"
    Then the command exits with code 0
    And the JSON output has "state" equal to "down"
    And the runtime port is free
    And no Roca process is left alive

  @fast
  Scenario: F01-06 Querying works without anything having been started
    Given La Roca is installed and initialized
    And the runtime is not started
    When I run "roca query 'cuantas memorias hay' --json"
    Then the command exits with code 0
    And the JSON output has "path" not empty
    And no resident process has been started

  @fast @acceptance
  Scenario: F01-07 uninstall keeping data leaves the machine usable
    Given La Roca is installed and initialized with data
    When I run "roca uninstall --keep-data --json"
    Then the command exits with code 0
    And the JSON output has "purged" equal to "false"
    And the database still exists
    And the binary is no longer linked in the binaries directory

  @fast @acceptance
  Scenario: F01-08 uninstall with purge leaves the machine at zero
    Given La Roca is installed and initialized with data
    And the runtime is started
    When I run "roca uninstall --purge --json"
    Then the command exits with code 0
    And the JSON output has "stopped" equal to "true"
    And the JSON output has "purged" equal to "true"
    And the JSON output lists every deleted path
    And no Roca artefact is left in the HOME
    And no Roca process is left alive
    And the runtime port is free
    And searching for "roca" in the HOME returns nothing

  @fast
  Scenario: F01-09 The purge can be re-run without punishing the operator
    Given La Roca is installed and initialized with data
    When I run "roca uninstall --purge --json"
    And I run "roca uninstall --purge --json" a second time
    Then the second command exits with code 0
    And the JSON output has "purged" equal to "true"
    And the output contains no traceback

  @fast @acceptance
  Scenario: F01-10 A half installation converges on the next one
    Given La Roca is installed and initialized
    When I launch the installer of a new version and kill it with SIGKILL halfway
    Then the binary that was active still answers "roca --version"
    And the active version is still the previous complete one
    When I run the installer of that new version again and let it finish
    Then the command exits with code 0
    And "roca --version" reports the new version
    And no partial installation tree is left in the HOME

  @fast
  Scenario: F01-11 A previous complete installation is recognized and not redone
    Given La Roca is installed at the target version
    When I run the installer of that same version
    Then the command exits with code 0
    And the output contains "already installed"
    And the active binary has not changed inode

  @fast
  Scenario: F01-12 The installer never overwrites a file that is not its own
    Given a clean HOME with no trace of Roca
    And there is a regular file named "roca" in the binaries directory
    When I run the installer for the current platform
    Then the command exits with a code other than 0
    And the output names the file it refuses to overwrite
    And that file keeps its original content

  @fast @acceptance
  Scenario: F01-13 An aged database is adopted, not rejected
    Given a HOME with an aged Roca database carrying tables from withdrawn features
    When I run "roca init --json"
    Then the command exits with code 0
    And the JSON output has "database" equal to "adopted"
    And the JSON output lists the orphan tables it found
    And the output names the exact command to archive them
    And no data table has been dropped
    And no row has been deleted
    When I run "roca start --json"
    Then the JSON output has "problems" equal to "none"

  @fast
  Scenario: F01-14 Archiving orphans is explicit, with a copy first and destroying nothing
    Given a HOME with an aged Roca database carrying tables from withdrawn features
    And La Roca is initialized over that database
    When I run "roca schema archive-orphans --json"
    Then the command exits with code 0
    And the JSON output has "applied" equal to "false"
    And no table has been renamed
    When I run "roca schema archive-orphans --yes --json"
    Then the command exits with code 0
    And the JSON output names the backup created before touching anything
    And that copy exists and is a restorable database
    And every orphan table appears renamed, not dropped
    And the rows of the renamed tables are still there

  @acceptance @slow
  Scenario: F01-15 The whole cycle, twice in a row, with the same numbers
    Given a clean HOME with no trace of Roca
    When I walk the full cycle of installation, init, use, stop and purge
    And I walk the full cycle a second time
    Then both cycles exit with code 0 in every one of their steps
    And the ingest counts of the second cycle equal those of the first
    And no Roca artefact is left in the HOME
