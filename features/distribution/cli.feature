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
      | model     |
      | doctor    |
      | update    |
      | uninstall |
      | plugins   |
      | index     |

  Scenario: Neighbor executables extend unknown commands without intercepting built-ins
    When the operator exercises the plugin dispatch contract
    Then arguments, standard input, output, and exit status cross the plugin seam untouched
    And built-ins win, missing plugins explain the convention, and plugins lists the fixtures

  Scenario: Plugin lifecycle is inert until the experimental feature is enabled
    When the operator tries to install a plugin without enabling experimental plugins
    Then the installer is inert and names the feature flag

  Scenario: Init closes with one ordered, fully timed summary
    When the operator exercises the "init" command in human and JSON form
    Then init reports setup, ingest, index, model, and its total once in that order

  Scenario: Non-interactive init names the answering model without opening the chooser
    When the operator initializes non-interactively with a detected model CLI
    Then init prints one answering notice and writes no model configuration
