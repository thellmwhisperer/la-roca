# language: en

@journey @distribution
Feature: Exec time limit
  A SELECT that would scan for minutes is cancelled at the five-second bound
  so it cannot hold the database.

  Scenario: CLI exec cancels a runaway statement at five seconds
    Given La Roca is installed and initialized
    When I exec the SQL "WITH RECURSIVE costly(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM costly WHERE n < 100000000) SELECT sum(n) FROM costly"
    Then the command exits with a code other than 0
    And the output contains "the validated SQL exceeded the time limit after 5s"
    And the command finished within 6 seconds

  Scenario: MCP exec cancels a runaway statement at five seconds
    Given La Roca is installed and initialized
    When I open an MCP session over stdio against the binary
    And I call the exec tool with the SQL "WITH RECURSIVE costly(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM costly WHERE n < 100000000) SELECT sum(n) FROM costly"
    Then the response is a tool error
    And the readable response contains "the validated SQL exceeded the time limit after 5s"
    And the MCP call finished within 6 seconds

  Scenario: CLI exec times out a leading wildcard and supplies a runnable MATCH
    Given La Roca is installed and initialized
    And the corpus has orchard exchanges
    When I exec the SQL "SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE '%orchard%'" with a costly tail
    Then the command exits with a code other than 0
    And the output contains "the validated SQL exceeded the time limit after 100ms"
    And the output contains "or set --timeout-ms explicitly"
    And the output contains "plugin_roca_corpus.exchanges.human_text"
    And the output contains "exchanges_fts MATCH 'orchard'"
    And the command finished within 2 seconds
    When I run the printed roca exec line verbatim
    Then the command exits with code 0
    And the result contains exactly the orchard exchange IDs

  Scenario: CLI exec resolves a schema-qualified LIKE when joined tables share a name
    Given La Roca is installed and initialized
    When I exec the SQL "SELECT plugin_roca_corpus.memories.id FROM plugin_roca_ops.memories CROSS JOIN plugin_roca_corpus.memories WHERE plugin_roca_corpus.memories.content LIKE '%orchard%'" with a costly tail
    Then the command exits with a code other than 0
    And the output contains "the validated SQL exceeded the time limit after 100ms"
    And the output contains "or set --timeout-ms explicitly"
    And the output contains "leading-wildcard LIKE on plugin_roca_corpus.memories.content"
    And the printed roca exec line selects from corpus memories FTS
    When I run the printed roca exec line verbatim
    Then the command exits with code 0

  Scenario Outline: CLI exec hints after timing out leading wildcards with ESCAPE or parenthesised operands
    Given La Roca is installed and initialized
    And the corpus has orchard exchanges
    When I exec the SQL "<sql>" with a costly tail
    Then the command exits with a code other than 0
    And the output contains "the validated SQL exceeded the time limit after 100ms"
    And the output contains "or set --timeout-ms explicitly"
    And the output contains "plugin_roca_corpus.exchanges.human_text"
    And the output contains "exchanges_fts MATCH 'orchard'"
    And the command finished within 2 seconds
    When I run the printed roca exec line verbatim
    Then the command exits with code 0
    And the result contains exactly the orchard exchange IDs
    Examples:
      | sql |
      | SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE '%orchard%' ESCAPE '!' |
      | SELECT id FROM plugin_roca_corpus.exchanges WHERE (human_text) LIKE ('%orchard%') |
      | SELECT id FROM plugin_roca_corpus.exchanges WHERE (human_text) LIKE '%orchard%' |
      | SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE ('%orchard%') |
      | SELECT id FROM plugin_roca_corpus.exchanges WHERE ((human_text)) LIKE (('%orchard%')) ESCAPE '!' |

  Scenario Outline: CLI exec leaves other LIKE scans available
    Given La Roca is installed and initialized
    And the corpus has orchard exchanges
    When I exec the SQL "<sql>"
    Then the command exits with code 0
    Examples:
      | sql |
      | SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE 'orchard%' |
      | SELECT id FROM plugin_roca_corpus.exchanges WHERE (human_text) LIKE ('orchard%') ESCAPE '!' |
      | SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE '%orchard%' ESCAPE ('%') |
      | SELECT id FROM plugin_roca_corpus.tool_uses WHERE tool_name LIKE '%orchard%' |
      | SELECT id FROM plugin_roca_corpus.exchanges WHERE model LIKE '%orchard%' |
      | WITH constants(human_text) AS (VALUES ('orchard')) SELECT human_text FROM constants WHERE human_text LIKE '%orchard%' |
      | SELECT 1 IS DISTINCT FROM 2 |

  Scenario: CLI exec recognizes the thinking blocks FTS twin
    Given La Roca is installed and initialized
    And the corpus has FTS suggestion edge cases
    When I exec the SQL "SELECT id FROM plugin_roca_corpus.thinking_blocks WHERE full_text LIKE '%orchard%'" with a costly tail
    Then the command exits with a code other than 0
    And the output contains "the validated SQL exceeded the time limit after 100ms"
    And the output contains "or set --timeout-ms explicitly"
    And the output contains "plugin_roca_corpus.thinking_blocks.full_text"
    And the output contains "thinking_fts MATCH 'orchard'"
    When I run the printed roca exec line verbatim
    Then the command exits with code 0
    And the output contains "rows[1]{id"
    And the output contains "92001"

  Scenario Outline: CLI exec quotes FTS operator words as search text
    Given La Roca is installed and initialized
    And the corpus has FTS suggestion edge cases
    When I exec the SQL "SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE '%<term>%'" with a costly tail
    Then the command exits with a code other than 0
    And the output contains "the validated SQL exceeded the time limit after 100ms"
    And the output contains "or set --timeout-ms explicitly"
    And the output contains "plugin_roca_corpus.exchanges.human_text"
    When I run the printed roca exec line verbatim
    Then the command exits with code 0
    And the output contains "rows[1]{id"
    And the output contains "<id>"
    Examples:
      | term | id    |
      | OR   | 92011 |
      | AND  | 92012 |
      | NOT  | 92013 |

  Scenario: CLI exec permits an explicit timeout for a leading wildcard
    Given La Roca is installed and initialized
    And the corpus has orchard exchanges
    When I exec the SQL "SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE '%orchard%'" with timeout-ms 5000
    Then the command exits with code 0
    And the result contains exactly the orchard exchange IDs

  Scenario Outline: CLI exec resolves LIKE column ownership and quotes MATCH terms
    Given La Roca is installed and initialized
    And the corpus has orchard exchanges
    When I exec the SQL "<sql>" with a costly tail
    Then the command exits with a code other than 0
    And the output contains "the validated SQL exceeded the time limit after 100ms"
    And the output contains "or set --timeout-ms explicitly"
    And the output contains "plugin_roca_corpus.exchanges.human_text"
    When I run the printed roca exec line verbatim
    Then the command exits with code 0
    And the result contains exactly the orchard exchange IDs
    Examples:
      | sql |
      | SELECT e.id FROM plugin_roca_corpus.exchanges AS e WHERE e.human_text LIKE '%orchard!%' |
      | SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE '%orchard$%' |
      | SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE '%orchard''%' |
      | SELECT e.id FROM plugin_roca_corpus.tool_uses AS t JOIN plugin_roca_corpus.exchanges AS e ON e.id = t.id WHERE e.human_text LIKE '%orchard%' |
      | SELECT e.id FROM plugin_roca_ops.memories AS m JOIN plugin_roca_corpus.exchanges AS e ON e.id = m.id WHERE human_text LIKE '%orchard%' |

  Scenario: CLI exec preserves LIKE expressions that do not name an indexed column
    Given La Roca is installed and initialized
    And the corpus has orchard exchanges
    When I exec the SQL "SELECT id FROM plugin_roca_corpus.exchanges WHERE lower(human_text) LIKE '%orchard%'"
    Then the command exits with code 0
    And the result contains exactly the orchard exchange IDs

  Scenario: CLI exec permits a fast leading wildcard without an explicit timeout
    Given La Roca is installed and initialized
    And the corpus has orchard exchanges
    When I exec the SQL "SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE '%orchard%'"
    Then the command exits with code 0
    And the result contains exactly the orchard exchange IDs

  Scenario: CLI exec omits the LIKE hint for an explicit timeout
    Given La Roca is installed and initialized
    And the corpus has orchard exchanges
    When I exec the SQL "WITH RECURSIVE costly(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM costly WHERE n < 100000000) SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE '%orchard%' UNION ALL SELECT sum(n) FROM costly" with timeout-ms 20
    Then the command exits with a code other than 0
    And the output contains "the validated SQL exceeded the time limit after 20ms"
    And the output does not contain "use MATCH"
