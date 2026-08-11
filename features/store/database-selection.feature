# language: en

@store
Feature: Choosing the database for one command
  Each command uses the database chosen for that invocation. Without an
  explicit choice, La Roca uses the database in its home directory. Every
  answer identifies the database it came from.

  Scenario: A command can read a chosen database
    Given a home Roca database containing a memory unique to home
    And another Roca database containing a memory unique to that database
    When I search the other database for its unique memory
    Then the search returns the memory from the other database
    And the output identifies the other database as the one that answered

  Scenario: A command without an explicit database uses the home database
    Given a home Roca database containing a memory unique to home
    And another Roca database containing a memory unique to that database
    When I search without choosing a database for the command
    Then the search returns the memory from the home database
    And the output identifies the home database as the one that answered

  Scenario: A missing selected database points to initialization
    Given a selected path where no Roca database exists
    When I search that database
    Then the command exits with a code other than 0
    And the output says to run "roca init" before searching it
