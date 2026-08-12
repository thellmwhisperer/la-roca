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

  Scenario: Login and Doctor explain the external authentication boundary
    When I inspect login and Doctor help
    Then both help surfaces say models authenticate through their own CLIs
    And neither help surface advertises a stored model credential

  Scenario: Claude login only verifies the existing local session
    Given a fake Claude Code binary is available
    When I log in to "claude" with model "sonnet"
    Then the command exits with code 0
    And the configuration chooses model "sonnet" for "claude"
    And the output says La Roca stores no secrets
    And no model credential directory exists
