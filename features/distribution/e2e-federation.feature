@journey @e2e-federation
Feature: Frozen federation installed binary
  Commands against an installed roca binary on the frozen synthetic federation.
  The live hub is never selected. The fixture is testdata/e2e-federation/frozen.

  # E2E acceptance timings are informative trend data, not performance
  # budgets or release blockers: "si tarda cinco horas en hacer roca exec, sí quiero que se bloquee la release, pero si tarda 4,1 en vez de 4 cuando 4 es random, me toca la polla".
  # Each scenario records and prints its measured duration on every run
  # and machine, including passing runs. The only temporal cutoff is a
  # command exceeding 60 seconds; that cutoff is a hang guard, not a
  # performance budget. Functional gates stay blocking: exit code,
  # expected engines, no silent degrade to search hybrid, and ready-index use.

  Scenario: 321 ingest
    Given a frozen pr321 federation lab
    When I run "roca ingest --json"
    Then the command exits with code 0
    And the JSON output has "errors" equal to "0"
    When I exec the SQL "SELECT session_id, exchange_number FROM plugin_roca_corpus.exchanges WHERE session_id = '019aba72-aa57-7d93-a12c-b6e65c0dca6b' ORDER BY exchange_number" as json
    Then the command exits with code 0
    And the output contains "019aba72-aa57-7d93-a12c-b6e65c0dca6b"

  Scenario: 325 pill delete
    Given a pill-free frozen synthetic federation lab
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
    When I run "roca pill"
    Then the command exits with code 0
    And the output contains "no active pills"

  Scenario: 326 max-chars
    Given a frozen synthetic federation lab
    When I exec the SQL "SELECT content FROM plugin_roca_ops.memories WHERE project='budgets'" with max-chars 900
    Then the command exits with code 0
    And the output contains a digit run of at least 200 characters
    When I exec the SQL "SELECT content FROM plugin_roca_ops.memories WHERE project='budgets'" with max-chars 900 as json
    Then the command exits with code 0
    And the JSON output field "rows[0].content" has between 800 and 900 runes

  Scenario: 315 shared resident
    Given a frozen synthetic federation lab
    When I start three mcp serve processes
    Then one vector resident process exists

  Scenario: 317 unqualified table
    Given a frozen synthetic federation lab
    When I exec the SQL "SELECT content FROM memories WHERE content LIKE '%PROCESO ALPHA%'"
    Then the command exits with a code other than 0
    And the output contains "unqualified table"
    And the output contains "plugin_roca_ops.memories"
    And the output contains "plugin_roca_corpus.memories"

  Scenario: 318 handoff limit
    Given a frozen synthetic federation lab
    When I run "roca handoff latest --project harbor --limit 1"
    Then the command exits with code 0
    And the output contains "harbor"
    When I run "roca handoff latest --all-projects"
    Then the command exits with code 0
    And the output contains "harbor"
    And the output contains "dock"

  Scenario: 319 json ids and 427 short numeric ids and legacy lookup
    Given a frozen synthetic federation lab
    When I exec the SQL "SELECT id FROM plugin_roca_ops.memories WHERE id = '1152921504606846980'" as json
    Then the command exits with code 0
    And the JSON output field "rows[0].id" is a JS-safe integer of at most 12 digits
    When I call the exec tool with the SQL "SELECT id, legacy_id FROM plugin_roca_ops.memories WHERE id = '1152921504606846980'"
    Then the response is not an error
    And the readable MCP response contains "1152921504606846980"
    When I store a discovery over MCP superseding historical id "1152921504606846980"
    Then the response is not an error
    And the MCP stored id is a JS-safe integer of at most 12 digits
    When I exec the SQL "SELECT COUNT(*) AS n FROM plugin_roca_ops.memories replacement JOIN plugin_roca_ops.memories original ON replacement.supersedes = original.id WHERE replacement.content = 'MCP historical id replacement' AND original.legacy_id = 1152921504606846980" as json
    Then the command exits with code 0
    And the JSON output has "rows[0].n" equal to "1"

  Scenario: 4269 installed command on PATH
    Given a frozen synthetic federation lab
    When I run the installed roca version through PATH
    Then the command exits with code 0
    And the output contains "roca"

  Scenario: 324 exact Codex session id
    Given a frozen pr324 federation lab
    When I run "roca ingest --json"
    Then the command exits with code 0
    When I exec the SQL "SELECT session_id FROM plugin_roca_corpus.sessions WHERE session_id LIKE '019aba72-aa57-7d93-a12c-b6e65c0dca6%' ORDER BY session_id"
    Then the command exits with code 0
    And the output contains "019aba72-aa57-7d93-a12c-b6e65c0dca6b"
    Then the frozen Codex identity has 2 sessions, 1 exact source session, 0 split siblings, 8 exchanges, 48 tools, 48 orphan tools, 1 failed tool, and 1 control session
    When I run "roca ingest --json" a second time
    Then the frozen Codex identity is unchanged

  @provisioned
  Scenario: 232991 vector query
    Given a frozen synthetic federation lab
    When I vector-query "harbor lantern"
    Then the command exits with code 0
    And the vector query executed the ready index

  Scenario Outline: uso-de-la-roca correction
    Given a frozen synthetic federation lab
    When I run "<command>"
    Then the command exits with code 0

    Examples:
      | command |
      | roca exec SELECT COUNT(*) AS memories FROM plugin_roca_ops.memories |
      | roca doctor |
      | roca version |
      | roca query harbor lantern |

  Scenario Outline: uso-de-la-roca correction output <contract>
    Given a frozen synthetic federation lab
    When I run "<command>"
    Then the command exits with code 0
    And the output contains "<expected>"

    Examples:
      | contract | command                             | expected      |
      | 233508   | roca query harbor lantern --json    | engines       |
      | 259288   | roca handoff latest --project harbor | harbor        |
      | 1708690  | roca pill show uso-de-la-roca        | vectors first |

  Scenario: 233400 exec harbor lantern
    Given a frozen synthetic federation lab
    When I exec the SQL "SELECT content FROM plugin_roca_ops.memories WHERE content LIKE '%harbor lantern%'" with timeout-ms 5000
    Then the command exits with code 0

  @provisioned
  Scenario: 238277 vector query
    Given a frozen synthetic federation lab
    When I vector-query "harbor lantern"
    Then the command exits with code 0
    And the vector query executed the ready index

  Scenario: 244386 exec layers
    Given a frozen synthetic federation lab
    When I exec the SQL "SELECT layer, COUNT(*) AS n FROM plugin_roca_ops.memories GROUP BY layer"
    Then the command exits with code 0

  Scenario: 93762 query json
    Given a frozen synthetic federation lab
    When I run "roca query harbor lantern --json"
    Then the command exits with code 0

  Scenario: 19944 handoff
    Given a frozen synthetic federation lab
    When I run "roca handoff latest --project harbor"
    Then the command exits with code 0

  @provisioned
  Scenario: 5740 vector query
    Given a frozen synthetic federation lab
    When I vector-query "harbor lantern"
    Then the command exits with code 0
    And the vector query executed the ready index

  Scenario: 5950 exec limit
    Given a frozen synthetic federation lab
    When I exec the SQL "SELECT content FROM plugin_roca_ops.memories LIMIT 1"
    Then the command exits with code 0

  @provisioned
  Scenario: 4657 vector query
    Given a frozen synthetic federation lab
    When I vector-query "harbor lantern"
    Then the command exits with code 0
    And the vector query executed the ready index

  @provisioned
  Scenario: 125372 vector query first person
    Given a frozen synthetic federation lab
    When I vector-query "I inspected the harbor lantern"
    Then the command exits with code 0
    And the vector query executed the ready index

  Scenario: 126485 exec discovery
    Given a frozen synthetic federation lab
    When I exec the SQL "SELECT content FROM plugin_roca_ops.memories WHERE layer='discovery'"
    Then the command exits with code 0

  Scenario: 127663 doctor
    Given a frozen synthetic federation lab
    When I run "roca doctor"
    Then the command exits with code 0

  Scenario: 296656 doctor
    Given a frozen synthetic federation lab
    When I run "roca doctor"
    Then the command exits with code 0

  Scenario: 297007 doctor
    Given a frozen synthetic federation lab
    When I run "roca doctor"
    Then the command exits with code 0

  Scenario: 1658381 doctor
    Given a frozen synthetic federation lab
    When I run "roca doctor"
    Then the command exits with code 0

  Scenario: 1733215 handoff
    Given a frozen synthetic federation lab
    When I run "roca handoff latest --project harbor"
    Then the command exits with code 0

  Scenario: real-usage hooks
    Given a frozen synthetic federation lab
    When I run the claude authorship hook
    Then the command exits with code 0

  Scenario: real-usage exec exact ids
    Given a frozen synthetic federation lab
    When I exec the SQL "SELECT id, legacy_id FROM plugin_roca_ops.memories WHERE id = '1152921504606846980'" as json
    Then the command exits with code 0
    And the JSON output field "rows[0].id" is a JS-safe integer of at most 12 digits
    And the output contains "1152921504606846980"

  @provisioned
  Scenario: real-usage vector query
    Given a frozen synthetic federation lab
    When I warm the vector index and vector-query "harbor lantern"
    Then the command exits with code 0
    And the vector query executed the ready index

  Scenario: real-usage query no silent degrade
    Given a frozen synthetic federation lab
    When I run "roca query harbor lantern --json"
    Then the command exits with code 0
    And the output contains "engines"
    And the output does not contain "search hybrid"

  Scenario: real-usage handoff one per project
    Given a frozen synthetic federation lab
    When I run "roca handoff latest --project harbor"
    Then the command exits with code 0
    And the output contains "handoffs[1]"
    And the output contains "harbor"
    And the output does not contain "handoffs[2]"

  Scenario: real-usage mcp handoff refused
    Given a frozen synthetic federation lab
    When I open an MCP session as client "glm-5.2 (codex/slopslint-detector-a1)"
    And I store a session handoff over MCP with the same content the CLI accepts
    Then the response is a tool error
    And the refusal names the agent, surface, origin and why it was refused

  @provisioned
  Scenario: real-usage update+init smoke
    When I run the e2e-smoke operator path
    Then the command exits with code 0

  Scenario: real-usage mcp health
    Given a frozen synthetic federation lab
    When I call the health tool over stdio
    Then the response is not an error
    And the readable MCP response contains "health: pass"

  @provisioned
  Scenario: 588 residents started by an MCP session with another PATH
    Given a frozen synthetic federation lab installed by install.sh from the release channel
    And the lab is migrated and serves the cutover layout
    And an MCP session started with a PATH without ~/.local/bin has called roca_vector_query once
    When I run "roca query harbor lantern --json" with ~/.local/bin on PATH
    Then the command exits with code 0
    And the query engines include "vector"
    And the query notices do not include "vector plugin is not installed"
    And the query notices do not include "vector search unavailable"
    When I run "roca-vector --json query 'harbor lantern'" with ~/.local/bin on PATH
    Then the command exits with code 0
    And the vector hits include the harbor lantern exchange
    And the output does not contain "find the roca core executable on PATH"

  @provisioned
  Scenario: 588 an upgrade over running residents
    Given a frozen synthetic federation lab installed by install.sh at the published release
    And the lab is migrated and serves the cutover layout
    And an MCP session started with a PATH without ~/.local/bin has called roca_vector_query once
    And that MCP session is closed
    When the candidate is installed by install.sh over it
    And I run "roca query harbor lantern --json" with ~/.local/bin on PATH
    Then the command exits with code 0
    And the query engines include "vector"
    And the query notices do not include "vector plugin is not installed"
    And the query notices do not include "vector search unavailable"

  @provisioned
  Scenario: 588 an MCP session that stays open across the upgrade
    Given a frozen synthetic federation lab installed by install.sh at the published release
    And the lab is migrated and serves the cutover layout
    And an MCP session started with a PATH without ~/.local/bin has called roca_vector_query once
    When the candidate is installed by install.sh over it
    And the MCP session calls roca_vector_query "harbor lantern" again
    Then the MCP vector hits include the harbor lantern exchange
    When I run "roca query harbor lantern --json" with ~/.local/bin on PATH
    Then the command exits with code 0
    And the query engines include "vector"

  @provisioned
  Scenario: 588 two queries right after the upgrade
    Given a frozen synthetic federation lab installed by install.sh at the published release
    And the lab is migrated and serves the cutover layout
    And an MCP session started with a PATH without ~/.local/bin has called roca_vector_query once
    When the candidate is installed by install.sh over it
    And I run two "roca query harbor lantern --json" at the same time with ~/.local/bin on PATH
    Then both queries exit 0 with "vector" among their engines
    And exactly one vector resident process runs for the home

  # Issue 589. roca_ops = false is the legacy home where roca store writes core.
  @provisioned
  Scenario: 589 a core memory and a corpus memory after the migration
    Given La Roca has just been installed from the release channel
    And a home made by roca init with vector = true and roca_ops = false, over an agent session that carries a memory mentioning "harbor lantern"
    And the operator ran roca store --layer discovery --content "orchard compass note" before the migration
    And roca migrate is verified and serving = "cutover" is set under layout
    When I run "roca vector install"
    Then the command exits with code 0
    When I run "roca vector status --json"
    Then the ops sidecar has some embedded chunks and a state other than "outdated"
    When I vector-query "orchard compass note" in databases "ops"
    Then the vector hits holding "orchard compass note" are one memory from "ops"
    When I vector-query "harbor lantern" in databases "corpus,ops"
    Then the vector hits holding "harbor lantern" are one memory from "corpus"

  # A home whose config held only vector = true before init does not serve ops.
  @provisioned
  Scenario: The first vector pass on a new home without roca_ops
    Given La Roca has just been installed from the release channel
    And a home made by roca init with vector = true, over an agent session that carries a memory mentioning "harbor lantern"
    And the operator ran roca store --layer discovery --content "orchard compass note" before the migration
    When I run "roca vector install"
    Then the command exits with code 0
    And the last vector worker line says "exit 0" and the worker log holds no "is not a table this query can read"
    When I run "roca vector status --json"
    Then vector status lists no "roca-ops" database and no state "empty" or "outdated"
    When I run "roca doctor"
    Then the output does not contain "roca vector install"

  @provisioned
  Scenario: Finding the stored memory before the migration
    Given La Roca has just been installed from the release channel
    And a home made by roca init with vector = true, over an agent session that carries a memory mentioning "harbor lantern"
    And the operator ran roca store --layer discovery --content "orchard compass note" before the migration
    When I run "roca vector install"
    Then the command exits with code 0
    When I run "roca query 'orchard compass' --json"
    Then the command exits with code 0
    And a query hit from "core.memories" holds "orchard compass note"
    When I run "roca vector query 'orchard compass' 5"
    Then the command exits with code 0
    And the output contains "database core has no vector declaration; continuing with FTS-only"
    And the output does not contain "unknown database"

  @provisioned
  Scenario: 589 an ops sidecar with no memories of its own
    Given La Roca has just been installed from the release channel
    And a home made by roca init with vector = true, over an agent session that carries a memory mentioning "harbor lantern"
    And roca migrate is verified and serving = "cutover" is set under layout
    When I run "roca vector install"
    Then the command exits with code 0
    When I run "roca vector status --json"
    Then the ops sidecar has 0 embedded chunks and a state other than "outdated"
    When I run "roca doctor"
    Then the output does not contain "roca vector install"

  # resident-follows-config: config.toml or the roca executable changes while
  # an agent session keeps the core resident alive.
  @provisioned
  Scenario: Turning vectors on while an agent session keeps the resident alive
    Given La Roca has just been installed from the release channel
    And a home made by roca init with vector = false, over an agent session that carries a memory mentioning "harbor lantern"
    And an MCP session started with a PATH without ~/.local/bin has called roca_query "harbor lantern" once
    When vector is switched on under features
    And I run "roca vector install"
    Then the command exits with code 0
    And the last vector worker line says "exit 0" and the worker log holds no "is not a table this query can read"
    When I run "roca query 'harbor lantern' --json" with ~/.local/bin on PATH
    Then the command exits with code 0
    And the query engines include "vector"
    And the query notices do not include "vector index is not installed"
    When I run "roca vector query 'harbor lantern' 3" with ~/.local/bin on PATH
    Then the command exits with code 0
    And the output contains "north pier"
    And the output does not contain "semantic search is disabled"

  @provisioned
  Scenario: The open agent session after the change
    Given La Roca has just been installed from the release channel
    And a home made by roca init with vector = false, over an agent session that carries a memory mentioning "harbor lantern"
    And an MCP session started with a PATH without ~/.local/bin has called roca_query "harbor lantern" once
    When vector is switched on under features
    And I run "roca vector install"
    Then the command exits with code 0
    And the last vector worker line says "exit 0" and the worker log holds no "is not a table this query can read"
    When the MCP session calls roca_query "harbor lantern" again
    Then the MCP query hits hold "north pier" with "vector" among their legs
    When I run "roca query 'harbor lantern' --json" with ~/.local/bin on PATH
    Then the command exits with code 0
    And the query engines include "vector"

  @provisioned
  Scenario: The roca executable replaced by hand
    Given a frozen synthetic federation lab installed by install.sh from the release channel
    And an MCP session started with a PATH without ~/.local/bin has called roca_query "harbor lantern" once
    When the operator copies another build of roca over ~/.local/bin/roca without the installer
    And I run "roca doctor" with ~/.local/bin on PATH
    Then the command exits with code 0
    And roca doctor reports a resident whose version is the one roca --version prints, or no resident
