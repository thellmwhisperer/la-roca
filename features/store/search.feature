# language: en

@store
Feature: Finding what was stored
  Search is the read half of the product: the words the operator types, matched
  against the index as whole words, folded across accents in any language, ranked
  with the best answer first, and honest when there is nothing to find.

  Scenario: Search matches whole words, never substrings inside other words
    Given a fresh Roca database
    And the model always defers to literal search
    And a memory in layer "project" with content "the category list needs review"
    And a memory in layer "project" with content "please feed the cat before you leave"
    When I search for "cat"
    Then the search returns exactly 1 result
    And the first result contains "feed the cat"
    And no result contains "category"

  Scenario Outline: Accented text matches its unaccented spelling, any language
    Given a fresh Roca database
    And the model always defers to literal search
    And a memory in layer "discovery" with content "<seeded>"
    When I search for "<query>"
    Then the search returns at least one result
    And the first result contains "<accent>"

    Examples:
      | seeded                          | query | accent |
      | the naïve façade is ready       | naive | naïve  |
      | das portal läuft über das netz  | uber  | über   |

  Scenario: Results rank by relevance, best first
    Given a fresh Roca database
    And the model always defers to literal search
    And a memory in layer "project" with content "release release release the canonical zebra marker"
    And a memory in layer "project" with content "the release came up once during a long talk about mountains and rivers"
    When I search for "release"
    Then the search returns at least one result
    And the first result contains "zebra"

  Scenario: No match is an honest zero, not an error
    Given a fresh Roca database
    And the model always defers to literal search
    When I search for "zzzzz"
    Then the command exits with code 0
    And the search returns no results
    And the JSON output declares the match was empty
