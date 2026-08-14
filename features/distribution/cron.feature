@acceptance @distribution
Feature: Cron plugin trains
  The cron plugin schedules no work itself; it previews, invokes, observes, and records plugin rides.

  Background:
    Given an isolated La Roca distribution

  Scenario: Dry-run aggregates rides and reports gates without taking the trip
    When the operator previews the default cron train with a gated plugin ride
    Then core ingest appears before the plugin ride and its dependency gate is closed
    And dry-run executes no ride and records no journey
