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
      | doctor    |
      | remote    |
      | update    |
      | uninstall |
      | plugins   |

  Scenario: Neighbor executables extend unknown commands without intercepting built-ins
    When the operator exercises the plugin dispatch contract
    Then arguments, standard input, output, and exit status cross the plugin seam untouched
    And built-ins win, missing plugins explain the convention, and plugins lists the fixtures

  Scenario: Plugin lifecycle is inert until the experimental feature is enabled
    When the operator tries to install a plugin without enabling experimental plugins
    Then the installer is inert and names the feature flag

  Scenario: Init closes with one ordered, fully timed summary
    When the operator exercises the "init" command in human and JSON form
    Then init reports setup, ingest, index, and its total once in that order

  Scenario: Non-interactive init leaves answering to the optional plugin
    When the operator initializes non-interactively with a detected model CLI
    Then init prints no answering notice and writes only the new-install features

  Scenario: Doctor report is a privacy-safe pasteable snapshot
    When the operator asks for a doctor support report
    Then the report is one fenced block with a federation mode and the JSON form is one document

  Scenario: Playground without its optional plugin prints the install hint
    When the operator asks playground without its plugin
    Then the command names the playground installation command

  Scenario: Help lists exec and vector
    When the operator asks for command-line help
    Then the command list has an "exec" line and a "vector" line, each with a description
    And a vector call the companion does not take names how to turn vector search on

  Scenario: The session hook says an unqualified table is refused
    When the operator runs the codex session hook
    Then the hook does not say an unqualified table returns 0 rows and says it is refused with the qualified candidates

  Scenario: The session hook names each attached alias and its tables
    When the operator runs the codex session hook
    Then the hook carries one alias line naming plugin_roca_corpus with sessions and exchanges and plugin_roca_ops with memories
    And every alias the hook names is attached to roca exec

  Scenario: The session hook without a database still carries the craft
    When the operator runs the codex session hook without a database
    Then the hook carries the craft text and no alias line
