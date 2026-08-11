# language: en

@store
Feature: Resilience
  The database is shared by every process on the machine. Two of them writing at
  once must not lose a transaction or corrupt a row, and a file that is not a
  Roca database at the database path must be refused with a reason an operator
  can read and act on.

  Scenario: Two concurrent writers never corrupt the database
    Given a fresh Roca database
    When two writers store different memories at the same time
    Then both writes succeed
    And the database holds both memories intact

  Scenario Outline: A corrupt or foreign file at the database path is refused with a plain reason
    Given a clean HOME with a "<kind>" file at the database path
    When I initialize the database
    Then the command exits with a code other than 0
    And the output names "<reason>"

    Examples:
      | kind    | reason                 |
      | corrupt | file is not a database |
      | foreign | not a Roca database    |
