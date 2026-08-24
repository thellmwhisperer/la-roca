# language: en

@store
Feature: Bootstrapping a database
  Init is the one command that turns an empty directory into a Roca database,
  and it is the one command an operator runs more than once. It has to leave the
  complete schema behind, say what it did in prose an operator can read, change
  nothing when it runs twice over the same database, and protect what is already
  there with a backup before it repairs it.

  Scenario: Init creates a fresh database with the complete schema
    Given a clean HOME
    When I initialize the database
    Then the command exits with code 0
    And the database holds every v1 table

  Scenario: Init narrates in plain text, never JSON
    Given a clean HOME
    When I initialize the database without JSON
    Then the command exits with code 0
    And the output is plain text, not JSON

  Scenario: Init does not return until word search answers
    Given a clean HOME
    When I initialize the database without JSON
    Then the command exits with code 0
    And init reports the word search round trip
    And init never reports the word index as broken

  Scenario: Bare init stops at working word search
    Given a clean HOME
    When I initialize the database without JSON
    Then the command exits with code 0
    And init did not start reading the history for meaning
    And the configuration leaves the meaning pass switched off

  Scenario: Running init twice leaves the database intact
    Given a fresh Roca database
    And a memory in layer "project" with content "the survivor note about kites"
    When I initialize the database again
    Then the command exits with code 0
    And the memory is still there

  Scenario: Init adopts an existing database by path, taking a backup first
    Given a Roca database that needs one structural repair
    When I initialize that database
    Then the command exits with code 0
    And a backup was taken before the repair
    And the repair left the schema current
