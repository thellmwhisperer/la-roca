@acceptance @distribution
Feature: Distribution lifecycle
  Installation changes only declared artefacts, and departure follows the operator's consent.

  Background:
    Given an isolated La Roca distribution

  Scenario: Update without a reachable release fails plainly and changes nothing
    When update checks an unreachable synthetic release endpoint
    Then update fails plainly and the installation is unchanged

  Scenario: Uninstall removes La Roca and keeps the data unless the operator consents; purge removes everything La Roca ever created, zero residue
    Given two synthetic homes with every La Roca integration installed
    When one home uninstalls with data kept and the other consents to purge
    Then the first keeps only its data and the second has zero La Roca residue

  Scenario: The install script and the Go code agree on every artefact name
    When the installer artefact catalogue is compared with the release code
    Then every platform has one identical artefact name

  Scenario Outline: Row answers respect the operator's character budget, on every surface
    Given a row with text longer than the operator's budget
    When the row is requested with a 48 character budget over "<surface>"
    Then no returned text field exceeds 48 characters

    Examples:
      | surface  |
      | terminal |
      | MCP      |
