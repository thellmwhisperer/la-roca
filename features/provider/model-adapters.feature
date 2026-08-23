# language: en

@journey @model
Feature: Frontier through local agent CLIs with a local floor
  As an operator with or without an installed agent CLI
  I want La Roca to use the best available local command and drop to Ollama when needed
  so that the machine can always answer and I always know what answered.

  Background:
    Given La Roca is installed and initialized
    And a HOME with the seeded world "synthetic-corpus"

  @fast
  Scenario: A detected and signed-in frontier CLI is used before Ollama
    Given the frontier agent CLI is available
    And the local model is available too
    When I run "roca playground 'what decisions were made about the format' --json"
    Then the command exits with code 0
    And the JSON output has "sql_provider" equal to the frontier provider
    And the JSON output has "path" equal to "model"
    And the local provider has received no request

  @fast
  Scenario: A detected CLI whose request fails falls to the local model unaided
    Given the frontier agent CLI fails when asked
    And the local model is available
    When I run "roca playground 'what decisions were made about the format' --json"
    Then the command exits with code 0
    And the JSON output has "sql_provider" equal to "ollama"
    And the JSON output has "path" equal to "model"
    And the output declares that it degraded to the local floor
    And no action has been asked of the operator

  @fast
  Scenario: Without a frontier CLI, the factory order uses the local model
    Given the frontier agent CLI is not installed
    And the local model is available
    When I run "roca playground 'what decisions were made about the format' --json"
    Then the command exits with code 0
    And the JSON output has "sql_provider" equal to "ollama"

  @fast
  Scenario: With no agent CLI and no local model, the failure is clear and actionable
    Given the frontier agent CLI is not installed
    And the local model is not available
    When I run "roca playground 'what decisions were made about the format' --json"
    Then the command exits with a code other than 0
    And the JSON output has "degraded" equal to "model_unavailable"
    And the output names which providers were tried and why each one failed
    And the output names the exact command to install or start the local model
    And the output contains no traceback

  @fast
  Scenario: An unknown provider in the configuration kills nothing
    Given the configuration declares a provider this version does not know
    When I run "roca playground 'how many memories are there' --json"
    Then the output contains a warning that names the unknown provider
    And that warning lists the available providers

  @fast
  Scenario: Model set verifies a CLI and persists its answering model without storing authentication
    When I set "codex" to model "gpt-5.6-luna"
    Then the configuration chooses model "gpt-5.6-luna" for provider "codex"
    And the model set output names the model and its configuration source

  @fast
  Scenario: Doctor names the chosen CLI model, its source and both ways to change it
    Given the configuration chooses model "frontier-demo" for the frontier provider
    When I run "roca doctor"
    Then the model narration names "frontier-demo", its configuration source and both ways to change it

  @fast
  Scenario: A configured model wins over the CLI preset default
    Given the frontier agent CLI is available
    And the configuration chooses model "frontier-demo" for the frontier provider
    When I run "roca playground 'what decisions were made about the format' --json"
    Then the command exits with code 0
    And the JSON output has "sql_model" equal to "frontier-demo"
