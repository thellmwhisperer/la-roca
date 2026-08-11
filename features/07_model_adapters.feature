# language: en

@model
Feature: Frontier with a local floor
  As an operator with or without credentials and with or without network
  I want La Roca to use the best available model and drop to the local one when needed
  so that the machine can always answer, and so that I always know what it answered with.

  Background:
    Given La Roca is installed and initialized
    And a HOME with the seeded world "synthetic-corpus"

  @fast
  Scenario: F07-01 With a credential and network, the frontier provider is used
    Given there is a valid credential for the frontier provider
    And the frontier provider is available
    And the local model is available too
    When I run "roca query 'what decisions were made about the format' --json"
    Then the command exits with code 0
    And the JSON output has "sql_provider" equal to the frontier provider
    And the JSON output has "path" equal to "model"
    And the local provider has received no request

  @fast
  Scenario: F07-02 Without network, the cascade drops to the local model unaided
    Given there is a valid credential for the frontier provider
    And there is no network
    And the local model is available
    When I run "roca query 'what decisions were made about the format' --json"
    Then the command exits with code 0
    And the JSON output has "sql_provider" equal to "ollama"
    And the JSON output has "path" equal to "model"
    And the output declares that it degraded to the local floor
    And no action has been asked of the operator

  @fast
  Scenario: F07-03 Without a credential, the cascade drops to the local model
    Given there is no frontier provider credential
    And the local model is available
    When I run "roca query 'what decisions were made about the format' --json"
    Then the command exits with code 0
    And the JSON output has "sql_provider" equal to "ollama"

  @fast
  Scenario: F07-04 With no frontier and no local, the failure is clear and actionable
    Given there is no frontier provider credential
    And the local model is not available
    When I run "roca query 'what decisions were made about the format' --json"
    Then the command exits with a code other than 0
    And the JSON output has "degraded" equal to "model_unavailable"
    And the output names which providers were tried and why each one failed
    And the output names the exact command to install or start the local model
    And the output contains no traceback

  @fast
  Scenario: F07-06 The provider order is decided by the configuration, not by the code
    Given the configuration declares the provider order
    When I run "roca doctor --json"
    Then the JSON output lists the providers in the declared order
    And for each one it declares whether it is available and why

  @fast
  Scenario: F07-07 An unknown provider in the configuration kills nothing
    Given the configuration declares a provider this version does not know
    When I run "roca query 'how many memories are there' --json"
    Then the output contains a warning that names the unknown provider
    And that warning lists the available providers

  @fast
  Scenario: F07-10 Login persists the answering model without replacing the configuration
    When I log in to "xai" with model "grok-demo"
    Then the configuration chooses model "grok-demo" for provider "xai"
    And the login output names the model, its configuration source and both ways to change it

  @fast
  Scenario: F07-11 Doctor names the chosen model, its source and both ways to change it
    Given the configuration chooses model "frontier-demo" for the frontier provider
    When I run "roca doctor"
    Then the model narration names "frontier-demo", its configuration source and both ways to change it

  @fast
  Scenario: F07-12 A configured model wins over the provider's built-in default
    Given there is a valid credential for the frontier provider
    And the frontier provider is available
    And the configuration chooses model "frontier-demo" for the frontier provider
    When I run "roca query 'what decisions were made about the format' --json"
    Then the command exits with code 0
    And the JSON output has "sql_model" equal to "frontier-demo"
