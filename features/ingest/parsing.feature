# language: en

@acceptance @ingest
Feature: Normalizing each family
  Supported artefacts become the common session and memory records without
  turning configuration files or corrupt input into corpus content.

  Scenario: A Claude session becomes sessions, exchanges, thinking blocks and tool calls
    Given a Claude session with reasoning and a tool call is ready to ingest
    When I run ingest
    Then one session, one exchange, one thinking block and one tool call exist

  Scenario Outline: The durable memory files an agent leaves behind become memories
    Given a "<family>" memory file with durable content is ready to ingest
    When I run ingest
    Then one memory from "<family>" exists

    Examples:
      | family |
      | claude |
      | codex  |

  Scenario: A Codex session becomes sessions and exchanges
    Given a Codex session is ready to ingest
    When I run ingest
    Then one Codex session and one exchange exist

  Scenario: OpenCode preserves message content and excludes telemetry
    Given an OpenCode session with message content and telemetry is ready to ingest
    When I run ingest
    Then every finished OpenCode message and its content exists once
    And the OpenCode report names its message coverage
    And no OpenCode telemetry entered the corpus

  Scenario: ZCode keeps visible messages, tools, timestamps and model attribution
    Given a ZCode desktop session is ready to ingest
    When I run ingest
    Then the ZCode session and every exchange carry their recorded provenance

  Scenario: An exchange carries the provenance its own source recorded
    Given a Codex rollout with runtime machinery is ready to ingest
    When I run ingest
    Then the exchange carries the model, the provider and the token counts of the rollout

  Scenario: Markdown memories become memories with their metadata
    Given a markdown memory with declared metadata is ready to ingest
    When I run ingest
    Then its content and declared metadata exist on one memory

  Scenario: A malformed file is skipped and counted, never fatal
    Given a malformed Claude session file is ready to ingest
    When I run ingest
    Then the command succeeds and counts one malformed file

  Scenario: CLAUDE.md and AGENTS.md instruction files are never ingested
    Given instruction files and one ordinary session are present
    When I run ingest
    Then no instruction file is recorded as content or ingest state
