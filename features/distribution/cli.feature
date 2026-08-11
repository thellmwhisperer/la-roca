@acceptance @distribution
Feature: Distribution command line
  The terminal surface is complete, explicit about output shape, and helpful on mistakes.

  Background:
    Given an isolated La Roca distribution

  Scenario: Help lists every command with one honest line each
    When the operator asks for command-line help
    Then every public command appears once with an honest one-line summary

  Scenario Outline: Every command speaks human by default and JSON only when asked
    When the operator exercises the "<command>" command in human and JSON form
    Then the default answer is human-readable and the requested answer is one JSON document

    Examples:
      | command   |
      | init      |
      | query     |
      | store     |
      | ingest    |
      | login     |
      | doctor    |
      | update    |
      | uninstall |

  Scenario: An unknown command fails with a pointer to help, never silently
    When the operator runs an unknown command
    Then it fails, names the unknown command, and points to help

  Scenario: Init closes with one ordered, fully timed summary
    When the operator exercises the "init" command in human and JSON form
    Then init reports setup, ingest, index, model, and its total once in that order
