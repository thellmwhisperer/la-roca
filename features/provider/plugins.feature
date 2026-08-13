Feature: Querying isolated plugin databases
  Plugin data stays in its own SQLite file while La Roca makes only relevant,
  truthful schemas available to the model under the existing read-only gate.

  Background:
    Given an initialized home with no model

  Scenario: A relevant plugin is qualified and its rows declare their database
    Given the synthetic plugin "well-formed" is installed
    And the provider configuration is:
      | provider | model             | availability |
      | ollama   | plugin-acceptance | ready         |
    And the model answers with SQL "SELECT title AS text FROM plugin_well_formed.receipts LIMIT 1"
    When I ask "Which receipts were recorded?"
    Then the command exits with code 0
    And the consulted databases are "core, plugin:well-formed"
    And the first row declares database "plugin:well-formed"

  Scenario: A lying semantic layer degrades with a warning instead of becoming queryable
    Given the synthetic plugin "lying" is installed
    And the memory "synthetic core fallback" exists
    And the provider configuration is:
      | provider | model             | availability |
      | ollama   | plugin-acceptance | ready         |
    And the model answers with SQL "SELECT content AS text FROM memories LIMIT 1"
    When I ask "Which synthetic invoices are outstanding?"
    Then the command exits with code 0
    And the consulted databases are "core"
    And a warning names the plugin "lying" and column "outstanding_cents"
