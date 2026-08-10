# language: en

@mcp
Feature: The MCP is a thin plug over the same core
  As an agent with no shell
  I want to discover La Roca's tools and query it over the protocol
  so that I get exactly what I would get from the command line.

  Background:
    Given La Roca is installed and initialized
    And a HOME with the seeded world "golden-corpus"

  @fast
  Scenario: F08-01 The server comes up on demand over stdio and announces who it is
    When I open an MCP session over stdio against the binary
    And I send "initialize"
    Then the response declares the server name
    And the response declares the product version, not a library's
    And the response declares the supported protocol version
    And the process exits when standard input is closed

  @fast
  Scenario: F08-02 Discovery returns exactly the decided surface
    When I open an MCP session over stdio against the binary
    And I send "tools/list"
    Then the response lists exactly the tools decided for v1
    And every tool has a non-empty description
    And every tool has a valid input schema
    And no tool that is not on the decided list appears

  @fast
  Scenario: F08-03 Querying over stdio
    When I open an MCP session over stdio against the binary
    And I call the query tool with the question "cuantas memorias hay"
    Then the response is not an error
    And the structured response has "path" equal to "unresolved"

  @fast
  Scenario Outline: F08-04 Exact parity between the two surfaces
    When I run "roca query '<query>' --json"
    And I call the query tool over stdio with the question "<query>"
    Then both responses have the same "path"
    And both responses have the same rows
    And both responses declare the same version and the same source SHA

    Examples:
      | query                        |
      | cuantas memorias hay         |
      | count memories by project    |
      | guiones largos               |
      | que feedback tenemos         |

  @fast
  Scenario: F08-05 Writing through the plug is writing through the product
    When I call the store tool over stdio with a new memory
    Then the response is not an error
    When I run "roca exec 'SELECT COUNT(*) AS n FROM memories WHERE supersedes IS NULL' --json"
    Then the count has gone up by one
    And the audit record of that write declares it came from the plug

  @fast
  Scenario: F08-06 Teaching through the plug is teaching through the product
    When I call the teach tool over stdio with a question and its template
    And I run "roca query" with that same question and "--no-llm"
    Then the JSON output has "path" equal to "compiler"

  @fast
  Scenario: F08-07 A missing argument is answered as a tool error, not as a crash
    When I open an MCP session over stdio against the binary
    And I call the query tool with no arguments
    Then the response is a tool error
    And the response names the missing argument
    And the session is still alive
    And a correct call right after it works

  @fast
  Scenario: F08-08 Read-only mode is respected on both surfaces
    Given La Roca is in read-only mode
    When I run "roca store --layer discovery --content 'no deberia entrar'"
    Then the command exits with a code other than 0
    And the output names read-only mode and the refused operation
    When I call the store tool over stdio
    Then the response is an error
    And that error says the same as the command line said
    And the memory count has not changed

  @fast
  Scenario: F08-09 No withdrawn tool comes back through the back door
    When I open an MCP session over stdio against the binary
    And I send "tools/list"
    Then no withdrawn tool appears in the list
    And for every withdrawn tool the command line that replaces it exists

  @acceptance
  Scenario: F08-10 The resident server and stdio give the same thing
    Given the runtime is started
    When I query the published endpoint with the question "cuantas memorias hay"
    And I query the same question over stdio
    Then both responses have the same rows
    And both responses have the same "sql"
