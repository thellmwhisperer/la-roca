# language: en

@acceptance @ingest
Feature: Doctor reports historical duplicate sessions
  Sibling sessions share every id character but the last, the start and the
  title. Doctor names them and the SELECT that lists them; it changes nothing.

  Scenario: Two sibling sessions give one duplicate-sessions warning
    Given an extracted Anthropic export with two sibling sessions is ready to ingest
    When I run ingest with the export path
    And I run doctor
    Then doctor warns of 1 extra row in 1 group with the SELECT that lists them

  Scenario: A shared id stem with different starts is not a duplicate
    Given an extracted Anthropic export is ready to ingest
    When I run ingest with the export path
    And I run doctor
    Then doctor carries no duplicate-sessions warning

  Scenario: Doctor leaves duplicate sessions in place
    Given an extracted Anthropic export with two sibling sessions is ready to ingest
    When I run ingest with the export path
    And I run doctor
    Then the corpus session and exchange counts are the same before and after doctor
