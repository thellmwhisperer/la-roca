# language: en

@store
Feature: Hybrid search finds the vector companion where roca vector does
  `roca query` looks for `roca-vector` in the managed install directory before
  PATH, like `roca vector` does, so a shell that never had ~/.local/bin on its
  PATH still gets the vector leg. Discovery scenarios use a stub companion;
  enabling vector after init uses the real companion with a local model fixture.

  Scenario: Vector can be enabled after init without an existing managed state directory
    Given a home initialized with an agent session that mentions "harbor lantern"
    And the real vector companion uses a local model download fixture
    And vector is switched on under features
    When I run 'roca vector query "what did we decide" 5'
    Then the command exits with code 0
    And the output does not contain "rerun the command"
    When I run 'roca vector query "what did we decide" 5'
    Then the command exits with code 0
    And the output does not contain "rerun the command"
    When I run 'roca vector install'
    Then the command exits with code 0
    And the output does not contain "rerun the command"
    And the managed vector state directory exists after the local model download

  Scenario: Hybrid search uses the companion in ~/.local/bin when PATH does not hold it
    Given a home initialized with an agent session that mentions "harbor lantern"
    And vector is switched on under features
    And a stub roca-vector that answers query with that exchange is in ~/.local/bin only
    And PATH does not contain ~/.local/bin
    When the operator runs roca query "harbor lantern" as json
    Then the command exits with code 0
    And the query engines include "vector"
    And the query notices do not include "vector plugin is not installed"

  Scenario: Hybrid search says the companion is missing when neither ~/.local/bin nor PATH holds it
    Given a home initialized with an agent session that mentions "harbor lantern"
    And vector is switched on under features
    And no roca-vector exists in ~/.local/bin or on PATH
    When the operator runs roca query "harbor lantern" as json
    Then the command exits with code 0
    And the query engines are only "fts"
    And the query notices include "vector plugin is not installed; continuing with FTS-only"
