Feature: Legacy provider authentication migration
  La Roca stores no model-provider secrets and existing configurations remain safe to run.

  Background:
    Given an initialized home with no model

  Scenario: A pre-existing API-key provider config degrades honestly without crashing
    Given a pre-existing "API key" provider configuration
    When I query through the legacy provider configuration
    Then the command exits with code 1
    And the legacy query output names the retired provider and why no model answered
    And the output contains no traceback

  Scenario: A pre-existing OAuth provider config degrades honestly without crashing
    Given a pre-existing "OAuth" provider configuration
    And a fake "codex" agent CLI binary is on PATH
    When I query through the legacy provider configuration
    Then the command exits with code 1
    And the legacy query output names the retired provider and why no model answered
    And the output contains no traceback

  Scenario: A stale credential file alone never disables a detected local CLI
    Given a pre-existing "stale credential" provider configuration
    And a fake "codex" agent CLI binary is on PATH
    When I inspect the model report
    Then the command exits with code 0
    And the retired provider remains usable
    And its only open proposal offers to remove the retired credential file

  Scenario: The first-run API-key proposal can be accepted
    Given a pre-existing "API key" provider configuration
    And a fake "codex" agent CLI binary is on PATH
    When I accept the first-run migration proposal
    Then the command exits with code 0
    And the legacy provider is migrated to "codex"

  Scenario: The first-run API-key proposal can be declined
    Given a pre-existing "API key" provider configuration
    And a fake "codex" agent CLI binary is on PATH
    When I decline the first-run migration proposal
    Then the command exits with code 0
    And the legacy provider configuration is unchanged

  Scenario: The first-run OAuth proposal can be accepted
    Given a pre-existing "OAuth" provider configuration
    And a fake "codex" agent CLI binary is on PATH
    When I accept the first-run migration proposal
    Then the command exits with code 0
    And the legacy provider is migrated to "codex"

  Scenario: The first-run OAuth proposal can be declined
    Given a pre-existing "OAuth" provider configuration
    And a fake "codex" agent CLI binary is on PATH
    When I decline the first-run migration proposal
    Then the command exits with code 0
    And the legacy provider configuration is unchanged

  Scenario: Model check and Doctor explain the external authentication boundary
    When I inspect model check and Doctor help
    Then both help surfaces say models authenticate through their own CLIs
    And neither help surface advertises a stored model credential

  Scenario: Claude model check only verifies the existing local session
    Given a fake Claude Code binary is available
    When I check model "claude"
    Then the command exits with code 0
    And the output says configuration was not changed
    And the output says La Roca stores no secrets
    And no model credential directory exists
