@data-split-oracle
Feature: DATA SPLIT compatibility oracle
  The ratified data split must preserve what CLI and MCP users observe.
  A sealed synthetic bundle defines that behavior before custody moves.

  Scenario: The CLI contract replays byte for byte after normalization
    Given the sealed DATA SPLIT synthetic fixture
    When the compatibility oracle records and replays the golden bundle
    Then the CLI golden cases cover query, rescue, ranking, store, SQL, warnings, identities, and failures

  Scenario: The MCP contract replays byte for byte after normalization
    Given the sealed DATA SPLIT synthetic fixture
    When the compatibility oracle records and replays the golden bundle
    Then the MCP golden cases cover query, exec, store, read-only enforcement, and failures

  Scenario: A changed golden is rejected instead of accepted as new behavior
    Given the sealed DATA SPLIT synthetic fixture
    When one byte of the signed golden bundle is changed
    Then the compatibility oracle rejects the changed bundle
