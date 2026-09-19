@journey @e2e-federation
Feature: Frozen federation installed binary
  Commands against an installed roca binary on the frozen synthetic federation.
  The live hub is never selected.

  Background:
    Given a frozen synthetic federation lab

  Scenario: 325 pill delete
    When I store a pill with slug tmp-x and content Temporary pill for issue 320 acceptance
    Then the command exits with code 0
    When I run "roca pill"
    Then the output contains "tmp-x"
    When I run "roca pill delete tmp-x"
    Then the command exits with code 0
    And the output contains "deleted: 1"
    When I exec the SQL "SELECT count(*) FROM plugin_roca_ops.memories WHERE json_extract(metadata,'$.pill_slug')='tmp-x'"
    Then the command exits with code 0
    And the output contains "0"

  Scenario: 326 max-chars
    When I exec the SQL "SELECT content FROM plugin_roca_ops.memories WHERE layer='handoff' LIMIT 1" with max-chars 900
    Then the command exits with code 0

  Scenario: 317 unqualified table
    When I exec the SQL "SELECT content FROM memories WHERE content LIKE '%PROCESO CARWOW%'"
    Then the command exits with a code other than 0
    And the output contains "unqualified table"
    And the output contains "plugin_roca_ops.memories"
    And the output contains "plugin_roca_corpus.memories"

  Scenario: 318 handoff limit
    When I run "roca handoff latest --project harbor --limit 1"
    Then the command exits with code 0
    And the output contains "harbor"
    When I run "roca handoff latest --all-projects"
    Then the command exits with code 0
    And the output contains "harbor"
    And the output contains "dock"

  Scenario: 319 json ids
    When I exec the SQL "SELECT id FROM plugin_roca_ops.memories LIMIT 1" as json
    Then the command exits with code 0
    And the output contains "id"

  Scenario: 324 exact Codex session id
    When I exec the SQL "SELECT session_id FROM plugin_roca_corpus.sessions WHERE session_id LIKE '019aba72-aa57-7d93-a12c-b6e65c0dca6%' ORDER BY session_id"
    Then the command exits with code 0
    And the output contains "019aba72-aa57-7d93-a12c-b6e65c0dca6b"

  Scenario: 232991 vector query
    When I vector-query "harbor lantern"
    Then the command exits with code 0

  Scenario Outline: uso-de-la-roca correction
    When I run "<command>"
    Then the command exits with code 0

    Examples:
      | command |
      | roca query harbor lantern --json |
      | roca exec SELECT COUNT(*) AS memories FROM plugin_roca_ops.memories |
      | roca handoff latest --project harbor |
      | roca doctor |
      | roca version |
      | roca query harbor lantern |
      | roca pill show uso-de-la-roca |
