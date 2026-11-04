# language: en

@store
Feature: A resident answers only with the configuration and build on disk
  The core resident an MCP session starts retires when config.toml changes, so
  the next CLI call and the open session answer with the configuration on disk.
  The stub companion answers one fixed hit; no scenario here loads the
  embedding model.

  Scenario: A resident started before config.toml changes answers with the configuration on disk
    Given a home initialized with an agent session that mentions "harbor lantern"
    And a stub roca-vector that answers query with that exchange is in ~/.local/bin only
    And an MCP session has called roca_query "harbor lantern" once
    When vector is switched on under features
    And the operator runs roca query "harbor lantern" as json
    Then the command exits with code 0
    And the query engines include "vector"
    And the query notices do not include "vector index is not installed"
    When the MCP session calls roca_query "harbor lantern" again
    Then the MCP answer is not an error and holds "engines fts,vector"
    When I run 'roca doctor --json'
    Then the command exits with code 0
    And the JSON output has no resident or a resident with vector true

  Scenario: The open agent session after config.toml changes
    Given a home initialized with an agent session that mentions "harbor lantern"
    And a stub roca-vector that answers query with that exchange is in ~/.local/bin only
    And an MCP session has called roca_query "harbor lantern" once
    When vector is switched on under features
    And the MCP session calls roca_query "harbor lantern" again
    Then the MCP answer is not an error and holds "engines fts,vector"
    When the operator runs roca query "harbor lantern" as json
    Then the command exits with code 0
    And the query engines include "vector"

  Scenario: Doctor with a resident alive
    Given a home initialized with an agent session that mentions "harbor lantern"
    And vector is switched on under features
    And an MCP session has called roca_query "harbor lantern" once
    When I run 'roca doctor'
    Then the command exits with code 0
    And the resident line names the version roca --version prints, "vector true" and "serving legacy-serving"
    When I run 'roca doctor --json'
    Then the command exits with code 0
    And the JSON resident names that build, config.toml, vector true, roca_ops true and serving "legacy-serving"

  Scenario: Doctor after the configuration changed
    Given a home initialized with an agent session that mentions "harbor lantern"
    And vector is switched on under features
    And an MCP session has called roca_query "harbor lantern" once
    When I run 'roca doctor --json'
    Then the JSON output has no resident or a resident with vector true
    When that MCP session is closed and its resident let it go
    And vector is switched off under features
    And I run 'roca doctor --json'
    Then the command exits with code 0
    And the JSON output has no resident or a resident with vector false
