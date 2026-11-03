# language: en

@store
Feature: Hybrid search fuses a plugin row found by both legs
  `roca query` fuses full text and vectors. A row the corpus serves carries the
  same database label on both legs, so the two legs meet on it once.

  Background:
    Given a home made by roca init that ingested an agent session mentioning "harbor lantern"
    And vector search is switched on
    And a roca-vector on PATH answers query with that exchange as its only hit

  Scenario: A corpus row found by both legs comes back once with consensus
    When I run 'roca query "harbor lantern" --databases corpus --json'
    Then the command exits with code 0
    And exactly one hit is that exchange from database "corpus" found by fts and vector with consensus

  Scenario: Require-both keeps a corpus row found by both legs
    When I run 'roca query "harbor lantern" --databases corpus --require-both --json'
    Then the command exits with code 0
    And the hits include that exchange with consensus
