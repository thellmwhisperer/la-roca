Feature: Provider selection
  Selection reports who would serve and why every earlier choice did not.

  Background:
    Given an initialized home with no model

  Scenario: Doctor names which model is going to answer, and why
    Given the provider configuration is:
      | provider | model                | availability |
      | xai      | frontier-acceptance  | unreachable  |
      | ollama   | local-acceptance     | ready         |
    When I run Doctor
    Then the command exits with code 0
    And Doctor names the titular model and explains the earlier failure

  Scenario: An unavailable frontier falls to the local floor, and says so
    Given the provider configuration is:
      | provider | model                | availability |
      | xai      | frontier-acceptance  | unreachable  |
      | ollama   | local-acceptance     | ready         |
    When I run Doctor
    Then the command exits with code 0
    And the titular provider is "ollama"
    And "xai" is unavailable before the ready local floor

  Scenario: With no provider at all, the failure names everything tried
    Given the provider configuration is:
      | provider | model                | availability |
      | xai      | frontier-acceptance  | unreachable  |
      | ollama   | local-acceptance     | unreachable  |
    When I ask "a question with no matching memory"
    Then the command exits with code 1
    And every configured provider is named with its failure reason
