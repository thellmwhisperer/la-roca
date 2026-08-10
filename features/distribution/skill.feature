@acceptance @distribution
Feature: Distribution agent teaching
  Skills and prompts are placed where agents can discover them without editing agent instructions.

  Background:
    Given an isolated La Roca distribution

  Scenario Outline: Skill install places the skill where the chosen agent discovers it, and says where
    When the operator installs the skill for "<agent>"
    Then only "<agent>" receives the canonical skill and the output names its path

    Examples:
      | agent    |
      | claude   |
      | codex    |
      | hermes   |
      | opencode |
      | pi       |

  Scenario: Nothing is ever installed into an agent without being asked
    When the operator requests a skill install without choosing an agent or all agents
    Then the request fails and every agent home remains without the skill

  Scenario: The agent prompt lands as prompt.md; La Roca never edits an agent's instruction files
    Given synthetic agent instruction files with operator-owned content
    When the operator initializes La Roca
    Then prompt.md is created and every agent instruction file is unchanged
    And init points to prompt.md without printing its contents
