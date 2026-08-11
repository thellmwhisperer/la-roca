# language: en

@acceptance @ingest
Feature: Official Anthropic data export
  Claude web and Desktop history enters the corpus only from export directories
  the operator explicitly declares.

  Scenario: A declared Anthropic export is ingested and re-ingest is a zero delta
    Given a declared Anthropic export is ready to ingest
    When I run ingest twice in a row
    Then the declared Anthropic export is ingested
    And the second ingest has a zero delta
    And doctor reports the export's older date as bedrock
