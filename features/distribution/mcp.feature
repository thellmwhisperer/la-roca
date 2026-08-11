@acceptance @distribution
Feature: Distribution MCP surface
  Agents receive the same service through a short-lived stdio process.

  Background:
    Given an isolated La Roca distribution

  Scenario: MCP serve exposes its tools over stdio and answers a health check
    When an agent opens the MCP stdio surface and requests its tools and health
    Then every declared MCP tool is described and the health answer is healthy

  Scenario: MCP row answers arrive as the same TOON rows the terminal shows
    Given a memory that identifies the TOON parity check
    When the terminal and MCP execute the same row query
    Then both readable answers carry the same TOON row properties

  Scenario: A memory stored over MCP is found by the next query
    When an agent stores a distinctive memory and immediately queries for it over MCP
    Then the query returns the memory stored by the preceding tool call
