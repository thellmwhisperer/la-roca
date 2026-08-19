Feature: Querying isolated plugin databases
  Plugin data stays in its own SQLite file while La Roca makes only relevant,
  truthful schemas available to the model under the existing read-only gate.

  Background:
    Given an initialized home with no model

  Scenario: A relevant plugin stays out of the default corpus-only answer
    Given the synthetic plugin "well-formed" is installed
    And the provider configuration is:
      | provider | model             | availability |
      | ollama   | plugin-acceptance | ready         |
    And the model answers with SQL "SELECT 1 AS answer LIMIT 1"
    When I ask "Which receipts were recorded?"
    Then the command exits with code 0
    And the consulted databases are "core, plugin:roca-corpus"

  Scenario: A lying semantic layer degrades with a warning instead of becoming queryable
    Given the synthetic plugin "lying" is installed
    And the memory "synthetic core fallback" exists
    And the provider configuration is:
      | provider | model             | availability |
      | ollama   | plugin-acceptance | ready         |
    And the model answers with SQL "SELECT 1 AS answer LIMIT 1"
    When I ask "Which synthetic invoices are outstanding?"
    Then the command exits with code 0
    And the consulted databases are "core, plugin:roca-corpus"
    And a warning names the plugin "lying" and column "outstanding_cents"
