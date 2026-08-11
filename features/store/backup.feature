# language: en

@store
Feature: Backup
  A backup is the precondition of any in-place repair, and it is the one
  artefact an operator carries to another machine. It has to land as a single
  whole, dated file, and that file has to open again with everything in it.

  Scenario: A backup lands as one dated file the operator can restore
    Given a Roca database that needs one structural repair
    When I initialize that database
    Then the command exits with code 0
    And exactly one dated backup file is left
    And the backup restores to the same memory count
