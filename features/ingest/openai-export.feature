# language: en

@acceptance @ingest
Feature: Official OpenAI data export
  ChatGPT history enters the corpus only from export directories the operator
  passes to an ingest command, and successive exports remain additive and idempotent.

  Scenario: A newer ChatGPT export contributes only new conversations and messages
    Given an extracted OpenAI export is ready to ingest
    When I run ingest with the export path twice in a row
    Then the second ingest has a zero delta
    When I select the newer OpenAI export and run ingest
    Then only the new ChatGPT conversations and messages are added
    And the ChatGPT exchanges retain OpenAI provenance

  Scenario: Every file in a sharded ChatGPT export is ingested
    Given an extracted sharded OpenAI export is ready to ingest
    When I run ingest with the export path
    Then every ChatGPT shard is ingested with OpenAI provenance

  Scenario: An explicit directory without a conversation layout is diagnosed
    Given an extracted OpenAI export has no conversation layout
    When I run ingest with the export path
    Then ingest names the unrecognized OpenAI export directory

  Scenario: A plain nightly ingest ignores leftover export configuration
    Given standing export paths remain in config
    When I run ingest
    Then no standing export is scanned
