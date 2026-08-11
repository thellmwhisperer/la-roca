Feature: Provider credentials
  API keys arrive after initialization and remain local secrets.

  Background:
    Given an initialized home with no model

  Scenario: An API-key login stores the key under the data directory at 0600, and never prints it
    When I log in to "xai" with the API key "provider-acceptance-secret"
    Then the command exits with code 0
    And the credential is stored under the data directory at 0600
    And no output contains the API key

  Scenario: Logout removes the credential and names what it removed
    Given the API key for "xai" has been stored through login
    When I log out from "xai"
    Then the command exits with code 0
    And the credential for "xai" is absent
    And the output names the removed credential for "xai"
