@acceptance @distribution
Feature: Distribution agent teaching
  Skills and prompts are placed where agents can discover them without editing agent instructions.

  Background:
    Given an isolated La Roca distribution

  Scenario Outline: Skill install places the skill where the chosen agent discovers it, and says where
    When the operator installs the skill for "<agent>"
    Then only "<agent>" receives the canonical skill and the output names its path
    And only "<agent>" receives the generated semantic catalog and no other runtime does

    Examples:
      | agent    |
      | claude   |
      | codex    |
      | cursor   |
      | grok     |
      | hermes   |
      | opencode |
      | pi       |
      | qwen     |

  Scenario: An installed skill is a registered artifact whose operator zone survives a refresh
    When the operator installs the skill for "claude"
    And the operator writes their own lines into the skill's operator zone
    And the operator installs the skill for "claude"
    Then the operator's lines survive, the product zone is canonical, and the registry records the skill

  Scenario: Nothing is ever installed into an agent without being asked
    When the operator requests a skill install without choosing an agent or all agents
    Then the request fails and every agent home remains without the skill

  Scenario: Every supported harness gets the same session hook, beside the hooks it already has
    Given every supported harness already has a session hook of its own
    When the operator installs the La Roca session hooks for every supported harness
    Then every harness carries the La Roca session hook beside the hook it already had
    And withdrawing them leaves every harness with only the hook it already had

  Scenario: The agent prompt lands as prompt.md; La Roca never edits an agent's instruction files
    Given synthetic agent instruction files with operator-owned content
    When the operator initializes La Roca
    Then prompt.md is created and every agent instruction file is unchanged
    And init points to prompt.md without printing its contents
