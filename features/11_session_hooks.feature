# language: en

@hooks @lifecycle
Feature: The session lifecycle feeds and is fed by La Roca
  As an agent starting a session on this machine
  I want to be handed what I should already know, under a measured budget
  so that continuity is served and not re-derived, and nobody's context window
  is flooded to do it.

  # Job J3 of the PRD, and the operator's decision on open question A-1, option
  # (b): the hooks enter v1 for the `claude` runtime, which is the only one the
  # laboratory ever supported. The law that governs every scenario here: a hook
  # reaches the kernel by running a command. Never the database directly, never
  # the MCP.

  @fast
  Scenario: F11-01 The hooks are declared in the runtime's own settings file
    Given La Roca is installed and initialized
    And the runtime "claude" has a settings file with a hook of its own in it
    When I run "roca hook install claude"
    Then the command exits with code 0
    And the settings file declares one command for each lifecycle event
    And the hook that was already there is still there
    And a backup of the previous settings file exists

  @fast
  Scenario: F11-02 A fresh session is handed the pills and the recent handoffs
    Given La Roca is installed and initialized
    And a HOME with the seeded world "session-lifecycle"
    When I run "roca hook context --json"
    Then the command exits with code 0
    And the output is valid JSON
    And the injected context contains the served pill
    And the injected context contains the most recent handoff
    And the injected context ends by pointing back at La Roca

  @fast
  Scenario: F11-03 The injection budget is a measured limit and not an intention
    Given La Roca is installed and initialized
    And a HOME with the seeded world "session-lifecycle"
    And there is a handoff longer than the whole budget
    When I run "roca hook context --json --max-chars 900"
    Then the command exits with code 0
    And the injected context does not exceed 900 characters
    And the budget report declares the limit that was applied
    And the budget report names every section that did not go in whole
    And the pill was served whole even so

  @fast
  Scenario: F11-04 The session start speaks the runtime's own protocol
    Given La Roca is installed and initialized
    And a HOME with the seeded world "session-lifecycle"
    When I run "roca hook context --runtime claude"
    Then the command exits with code 0
    And standard output is valid JSON and nothing else
    And the JSON output declares the lifecycle event it answers
    And the JSON output carries the injected context

  @fast
  Scenario: F11-05 What one session leaves, the next one is handed
    Given La Roca is installed and initialized
    When I run "roca hook record --trigger session_end --session-id abc-123 --cwd /w"
    Then the command exits with code 0
    When I run "roca hook handoff"
    Then the command exits with code 0
    And the output names the session that ended
    And the output names the working directory it ended in

  @fast
  Scenario: F11-06 The hook's only way into the kernel is the command line
    Given La Roca is installed and initialized
    And the runtime "claude" has a settings file with a hook of its own in it
    When I run "roca hook install claude"
    Then every command Roca declared is a roca command line
    And no command Roca declared names the database file
    And no command Roca declared speaks the MCP

  @fast
  Scenario: F11-07 A hook never breaks the session it runs in
    Given a HOME with no trace of Roca in it
    When I run "roca hook context --runtime claude"
    Then the command exits with code 0
    And standard output carries nothing
    When I run "roca hook record --trigger session_end"
    Then the command exits with code 0
    And the output contains no traceback

  # What Roca owns is the `hooks` member and nothing else, so that is the line
  # this scenario draws: outside it not one byte moves, and inside it whatever
  # the user declared is still declared and still runs. It is the laboratory's
  # own trade and the honest one: whoever owns a member owns how it is spelled.
  @fast
  Scenario: F11-08 Withdrawing the hooks leaves the rest of the file as it was
    Given La Roca is installed and initialized
    And the runtime "claude" has a settings file with a hook of its own in it
    When I run "roca hook install claude"
    And I run "roca hook uninstall claude"
    Then the command exits with code 0
    And no Roca hook is declared any more
    And the hook that was already there is still there
    And everything outside the hooks member is what it was
    And the settings file has not been deleted

  @fast
  Scenario: F11-09 Only the runtime the operator decided is supported
    Given La Roca is installed and initialized
    When I run "roca hook install codex"
    Then the command exits with a code other than 0
    And the output names the runtime that does have an adapter
    And the output contains no traceback
