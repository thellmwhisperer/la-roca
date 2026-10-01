# language: en

@acceptance @ingest
Feature: ZCode and Grok curated memories
  Project memory files and memory-v2 documents join the source matrix as
  memories. A missing directory is named. An unchanged file is not rewritten.

  Scenario: Synthetic ZCode and Grok memory files land once and stay incremental
    Given synthetic ZCode and Grok memory files are ready to ingest
    When I run a human ingest dry-run
    Then the dry-run shows ZCode saw more than one file and Grok memories
    When I run ingest
    Then ZCode and Grok each have memories and ZCode projects match their folders
    When I run ingest for a human
    Then the second ingest adds no ZCode or Grok memories

  Scenario: A missing ZCode or Grok memory directory is named, not silent
    Given ZCode and Grok session stores exist without memory directories
    When I run a human ingest dry-run
    Then the dry-run names the absent ZCode and Grok memory directories
