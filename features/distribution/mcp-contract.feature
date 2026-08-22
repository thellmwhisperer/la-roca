# language: en

@journey @mcp
Feature: The MCP is a thin plug over the same core
  As an agent with no shell
  I want to discover La Roca's tools and query it over the protocol
  so that I get exactly what I would get from the command line.

  Background:
    Given La Roca is installed and initialized
    And a HOME with the seeded world "synthetic-corpus"

  @fast
  Scenario: The server comes up on demand over stdio and announces who it is
    When I open an MCP session over stdio against the binary
    And I send "initialize"
    Then the response declares the server name
    And the response declares the product version, not a library's
    And the response declares the supported protocol version
    And the process exits when standard input is closed

  @fast
  Scenario: Discovery returns exactly the decided surface
    When I open an MCP session over stdio against the binary
    And I send "tools/list"
    Then the response lists exactly the tools decided for v1
    And every tool has a non-empty description
    And every tool has a valid input schema
    And no tool that is not on the decided list appears

  @fast
  Scenario: Querying over stdio
    When I open an MCP session over stdio against the binary
    And I call the query tool with the question "how many memories are there"
    Then the response is not an error
    And the response carries no structured content
    And the readable response is plain AXI text

  @fast
  Scenario Outline: Representative queries ship only readable AXI
    When I call the query tool over stdio with the question "<query>"
    Then the response is not an error
    And the response carries no structured content
    And the readable response is plain AXI text

    Examples:
      | query                        |
      | how many memories are there  |
      | count memories by project    |
      | synthetic release marker     |
      | what feedback do we have     |

  @fast
  Scenario: Writing through the plug is writing through the product
    When I call the store tool over stdio with a new memory
    Then the response is not an error
    When I run "roca exec 'SELECT (SELECT COUNT(*) FROM memories WHERE supersedes IS NULL) + (SELECT COUNT(*) FROM plugin_roca_ops.memories WHERE supersedes IS NULL) AS n' --json"
    Then the count has gone up by one
    And the identity card of that write declares it came from the plug

  @fast
  Scenario: A missing argument is answered as a tool error, not as a crash
    When I open an MCP session over stdio against the binary
    And I call the query tool with no arguments
    Then the response is a tool error
    And the response names the missing argument
    And the session is still alive
    And a correct call right after it works

  @fast
  Scenario: Read-only mode is respected on both surfaces
    Given La Roca is in read-only mode
    When I run "roca store --layer discovery --content 'this must be refused'"
    Then the command exits with a code other than 0
    And the output names read-only mode and the refused operation
    When I call the store tool over stdio
    Then the response is an error
    And that error says the same as the command line said
    And the memory count has not changed
