# language: en

@acceptance @ingest
Feature: Knowing what lives on this machine
  Ingest recognizes only the supported agent families and treats an absent
  family as an ordinary machine state.

  Scenario: Ingest detects the supported agent families present on this machine
    Given these supported agent families are present:
      | family          |
      | claude          |
      | claude-desktop  |
      | cowork          |
      | codex           |
      | opencode        |
      | zcode           |
      | pi              |
      | hermes          |
      | grok            |
    When I inspect ingest without writing
    Then exactly those agent families are detected

  Scenario: An absent family is reported as not found, never an error
    Given only the supported agent family "codex" is present
    When I inspect ingest without writing
    Then the supported agent family "pi" is reported as not found
    And ingest reports no errors
