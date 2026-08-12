# language: en

@acceptance @ingest
Feature: Official OpenAI data export
  ChatGPT history enters the corpus only from export directories the operator
  explicitly selects, and successive exports remain additive and idempotent.

  Scenario: A newer ChatGPT export contributes only new conversations and messages
    Given a declared OpenAI export is ready to ingest
    When I run ingest twice in a row
    Then the second ingest has a zero delta
    When I select the newer OpenAI export and run ingest
    Then only the new ChatGPT conversations and messages are added
    And the ChatGPT exchanges retain OpenAI provenance

  Scenario: Every file in a sharded ChatGPT export is ingested
    Given a declared sharded OpenAI export is ready to ingest
    When I run ingest
    Then every ChatGPT shard is ingested with OpenAI provenance

  Scenario: A declared directory without a conversation layout is diagnosed
    Given a declared OpenAI export has no conversation layout
    When I run ingest
    Then ingest names the unrecognized OpenAI export directory
