# language: en

@concurrency
Feature: Several agents writing at the same time
  As a fleet of agents sharing a single Roca
  I want to all write at once without coordinating
  so that no write is lost and no agent sees a lock error.

  Background:
    Given La Roca is installed and initialized

  @fast
  Scenario: F09-01 Eight simultaneous writers, no write lost
    Given I note the memory count
    When I launch 8 independent processes each writing 5 memories at once
    Then the 8 processes exit with code 0
    And the memory count has gone up by exactly 40
    And no error output contains "database is locked"
    And no error output contains "database is busy"

  @fast @slow
  Scenario: F09-02 Mixed writes and reads under contention
    When I launch 8 processes writing and 8 processes querying at once for 30 seconds
    Then every process exits with code 0
    And no query has returned an error
    And the final memory count equals the number of committed writes

  @fast
  Scenario: F09-03 A transaction that reads before writing does not lose its turn
    When I launch 8 processes that read and then write in the same transaction, at once
    Then the 8 processes exit with code 0
    And no transaction has been lost
    And no error output mentions a snapshot conflict

  @fast
  Scenario: F09-04 The database survives contention without corrupting
    When I launch 8 independent processes each writing 5 memories at once
    And I run the database integrity check
    Then the integrity check passes
    And the foreign key check passes

  @fast
  Scenario: F09-05 Writing from the command line and through the plug at once
    When I launch 4 processes writing from the command line and 4 sessions writing over stdio, at once
    Then they all finish without error
    And the memory count has gone up by the sum of the writes

  @fast
  Scenario: F09-06 Killing a writer halfway does not leave the database locked
    When I launch a process that writes and kill it with SIGKILL during the write
    And I run "roca store --layer discovery --content 'despues del muerto'"
    Then the command exits with code 0
    And the database integrity check passes
