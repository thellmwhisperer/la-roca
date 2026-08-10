# language: en

@store
Feature: Writing one memory
  Store is the write half of the product. One memory at a time, carrying its
  layer, its origin and its project; found again the moment it lands; honest
  about the content it will not accept; and able to mark one memory as replacing
  another.

  Scenario: Store writes one memory with layer, origin and project
    Given a fresh Roca database
    When I store a memory in layer "project" with origin "human", project "demo" and content "the canonical deployment note"
    Then the command exits with code 0
    And the stored memory has layer "project", origin "human" and project "demo"

  Scenario: A stored memory is immediately findable
    Given a fresh Roca database
    And the model always defers to literal search
    When I store a memory in layer "discovery" with content "the pineapple signal is unique"
    And I search for "pineapple"
    Then the search returns at least one result
    And the first result contains "pineapple"

  Scenario: Store refuses empty content
    Given a fresh Roca database
    When I store a memory in layer "project" with empty content
    Then the command exits with a code other than 0
    And the output names the missing content

  Scenario: A memory can supersede another; the old one stops answering
    Given a fresh Roca database
    And the model always defers to literal search
    When I store a memory in layer "project" with content "the original answer holds forty two"
    And I store a memory in layer "project" superseding the previous one with content "the corrected answer holds thirteen"
    And I search for "answer"
    Then the search returns exactly 1 result
    And the first result contains "forty two"

  Scenario: Supersedes pointing at a missing memory is refused
    Given a fresh Roca database
    When I store a memory in layer "project" superseding memory 999999 with content "points at a ghost"
    Then the command exits with a code other than 0
    And the output names the refused write
