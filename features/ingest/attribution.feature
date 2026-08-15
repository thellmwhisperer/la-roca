# language: en

@acceptance @ingest
Feature: Who wrote what
  Sessions keep a project, a supported family and any model identity their
  source explicitly declares.

  Scenario: Each session lands under the project its path declares
    Given a Claude session path declares project "anchor"
    When I run ingest
    Then that session belongs to project "anchor"

  Scenario: A session with no recognizable project lands global, and says so
    Given a Claude session path has no recognizable project
    When I run ingest
    Then that session has no project
    And the ingest report explains the global attribution

  Scenario: Every session records which agent family wrote it, from the supported list
    Given one session from every supported agent family is ready to ingest
    When I run ingest
    Then every session source is one of the supported agent families
    And every supported agent family owns a session

  Scenario Outline: Every session records which model answered when the artefact declares it
    Given a "<family>" session declares model "contract-model"
    When I run ingest
    Then that session records model "contract-model"

    Examples:
      | family         |
      | claude         |
      | claude-desktop |
      | codex          |
      | hermes         |
      | grok           |
