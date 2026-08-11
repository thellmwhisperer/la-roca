# language: en

@journey @model
Feature: Frontier with a local floor
  As an operator with or without credentials and with or without network
  I want La Roca to use the best available model and drop to the local one when needed
  so that the machine can always answer, and so that I always know what it answered with.

  Background:
    Given La Roca is installed and initialized
    And a HOME with the seeded world "synthetic-corpus"

  @fast
  Scenario: With a credential and network, the frontier provider is used
    Given there is a valid credential for the frontier provider
    And the frontier provider is available
    And the local model is available too
    When I run "roca query 'what decisions were made about the format' --json"
    Then the command exits with code 0
    And the JSON output has "engine" equal to the frontier provider
    And the JSON output has "path" equal to "llm_fallback"
    And the local provider has received no request

  @fast
  Scenario: Without a credential, the cascade drops to the local model
    Given there is no frontier provider credential
    And the local model is available
    When I run "roca query 'what decisions were made about the format' --json"
    Then the command exits with code 0
    And the JSON output has "engine" equal to "ollama"

  @fast
  Scenario: An unknown provider in the configuration kills nothing
    Given the configuration declares a provider this version does not know
    When I run "roca query 'how many memories are there' --json"
    Then the output contains a warning that names the unknown provider
    And that warning lists the available providers

  @fast
  Scenario: Login persists the answering model without replacing the configuration
    When I log in to "xai" with model "grok-demo"
    Then the configuration chooses model "grok-demo" for provider "xai"
    And the login output names the model, its configuration source and both ways to change it

  @fast
  Scenario: Doctor names the chosen model, its source and both ways to change it
    Given the configuration chooses model "frontier-demo" for the frontier provider
    When I run "roca doctor"
    Then the model narration names "frontier-demo", its configuration source and both ways to change it

  @fast
  Scenario: A configured model wins over the provider's built-in default
    Given there is a valid credential for the frontier provider
    And the frontier provider is available
    And the configuration chooses model "frontier-demo" for the frontier provider
    When I run "roca query 'what decisions were made about the format' --json"
    Then the command exits with code 0
    And the JSON output has "model" equal to "frontier-demo"
