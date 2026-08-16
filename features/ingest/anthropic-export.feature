# language: en

@acceptance @ingest
Feature: Official Anthropic data export
  Claude web and Desktop history enters the corpus only from export directories
  the operator passes to an ingest command.

  Scenario: An explicit Anthropic export is ingested and re-ingest is a zero delta
    Given an extracted Anthropic export is ready to ingest
    When I run ingest with the export path twice in a row
    Then the explicit Anthropic export is ingested
    And the second ingest has a zero delta
    And doctor reports the export's older date as bedrock

  Scenario: Project entities, current memories, and design chats land without inventing joins
    Given an extracted Anthropic export with project surfaces is ready to ingest
    When I run ingest with the export path
    Then Claude project entities documents and memories land
    And ordinary Claude conversations stay unprojected
    And the design chat keeps its project uuid
