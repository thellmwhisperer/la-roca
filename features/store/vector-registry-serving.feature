# language: en

@store
Feature: A vector command reads declarations that follow the serving layout
  The vector registry is the projection roca-vector reads. When the operator
  edits serving under [layout] by hand, the next `roca vector` command
  regenerates it before the companion starts, so in cutover the ops sidecar
  declares the core memories it owns. The stub companion answers status; no
  scenario here loads the embedding model.

  Scenario: A vector command after serving was switched to cutover declares the ops core memories
    Given a home initialized with an agent session that mentions "harbor lantern"
    And vector is switched on under features
    And the vector registry declares the ops tables "memories"
    And a stub roca-vector that answers status is in ~/.local/bin
    And serving = "cutover" is written under layout in config.toml
    When I run 'roca vector status'
    Then the command exits with code 0
    And the vector registry declares the ops tables "core_memories memories"

  Scenario: A vector command on a home that does not serve ops declares no ops sidecar
    Given a home initialized with an agent session that mentions "harbor lantern"
    And vector is switched on under features
    And the vector registry declares the ops tables "memories"
    And a stub roca-vector that answers status is in ~/.local/bin
    And roca_ops = false is written under features in config.toml
    When I run 'roca vector status'
    Then the command exits with code 0
    And the vector registry declares no ops sidecar

  Scenario: A vector command refuses to start the companion over a configuration it cannot read
    Given a home initialized with an agent session that mentions "harbor lantern"
    And the vector registry declares the ops tables "memories"
    And a stub roca-vector that answers status is in ~/.local/bin
    And config.toml is not valid TOML
    When I run 'roca vector status'
    Then the command exits with a code other than 0
    And the output contains "refresh vector declarations"
    And the output does not contain "stub roca-vector status"

  Scenario: A vector command refuses to start the companion over an ops database it cannot read
    Given a home initialized with an agent session that mentions "harbor lantern"
    And the vector registry declares the ops tables "memories"
    And a stub roca-vector that answers status is in ~/.local/bin
    And roca-ops.db is not a SQLite database
    When I run 'roca vector status'
    Then the command exits with a code other than 0
    And the output contains "inspect the DATA SPLIT writer fence"
    And the output does not contain "stub roca-vector status"
    When I run 'roca --read-only query "harbor lantern"'
    Then the command exits with a code other than 0
    And the output contains "inspect the DATA SPLIT writer fence"
