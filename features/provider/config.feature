Feature: Provider configuration
  Provider choices are operator-owned, optional, and safe to change over time.

  Background:
    Given an initialized home with no model

  Scenario: The provider order in config decides who is asked first
    Given a fake "codex" agent CLI binary is on PATH
    Given the provider configuration is:
      | provider | model             | availability |
      | ollama   | local-acceptance  | unreachable  |
      | codex    | frontier-contract | unreachable  |
    When I run Doctor
    Then the command exits with code 0
    And the reported provider order is "ollama, codex"

  Scenario: An unknown config key warns by its own name and is ignored
    Given the provider configuration is:
      | provider | model            | availability |
      | ollama   | local-acceptance | unreachable  |
    And the configuration also contains the unknown key "models.experimental_retries"
    When I run Doctor
    Then the command exits with code 0
    And the output warns about "models.experimental_retries"
    And the reported provider order is "ollama"

  Scenario: A missing config file means defaults, not a failure
    Given the configuration file is missing
    When I run Doctor
    Then the command exits with code 0
    And the configuration is reported as absent
    And the reported provider order is "ollama"

  Scenario: A detected agent CLI becomes the zero-login factory default
    Given a fake "codex" agent CLI binary is on PATH
    And the configuration file is missing
    When I run Doctor
    Then the command exits with code 0
    And the reported provider order is "codex, ollama"
    And the titular provider is "codex"
