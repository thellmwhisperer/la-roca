# language: en

@query @teach
Feature: Teaching La Roca a new question
  As an operator who sees a question of theirs fall to the model every time
  I want to teach it that question just once
  so that from then on it answers instantly and without a model.

  Background:
    Given La Roca is installed and initialized
    And a HOME with the seeded world "golden-corpus"

  @acceptance @slow
  Scenario: F06-01 Teaching moves a question from the slow route to the fast one
    Given the question "cuantas herramientas se han usado en total" is answered today by the model
    When I run "roca query 'cuantas herramientas se han usado en total' --json"
    Then the JSON output has "path" equal to "llm_fallback"
    And I note the latency of that answer
    When I run "roca teach --question 'cuantas herramientas se han usado en total' --template count_tool_uses_total --json"
    Then the command exits with code 0
    And the JSON output has "action" equal to "created"
    When I run "roca query 'cuantas herramientas se han usado en total' --json --no-llm"
    Then the command exits with code 0
    And the JSON output has "path" equal to "compiler"
    And the JSON output has "queryplan" naming the template "count_tool_uses_total"
    And the latency is at least a hundred times lower than the noted one
    And the rows are equal to the ones the model returned

  @fast
  Scenario: F06-02 Teaching the same question twice does not duplicate
    When I run "roca teach --question 'cuantas memorias tenemos guardadas' --template count_memories --json"
    And I run "roca teach --question 'cuantas memorias tenemos guardadas' --template count_memories --json"
    Then the second command exits with code 0
    And the JSON output of the second has "action" other than "created"
    And there is only one taught example for that question

  @fast
  Scenario: F06-03 Teaching a template that does not exist is refused and leaves no trace
    When I run "roca teach --question 'una pregunta cualquiera' --template plantilla_inventada --json"
    Then the command exits with a code other than 0
    And the output names the unknown template
    And the output lists the available templates
    And no example has been stored
    And the output contains no traceback

  @fast
  Scenario: F06-04 What was taught survives a process restart
    When I run "roca teach --question 'cuantas memorias tenemos guardadas' --template count_memories --json"
    And I restart the runtime
    And I run "roca query 'cuantas memorias tenemos guardadas' --json --no-llm"
    Then the JSON output has "path" equal to "compiler"

  @fast
  Scenario: F06-05 What was taught survives losing the working cache
    When I run "roca teach --question 'cuantas memorias tenemos guardadas' --template count_memories --json"
    And I delete the classifier's working cache directory
    And I run "roca query 'cuantas memorias tenemos guardadas' --json --no-llm"
    Then the command exits with code 0
    And the JSON output has "path" equal to "compiler"
    And the output contains no traceback

  @fast
  Scenario: F06-06 Teaching does not break what was already known
    Given I note the answer to every query of the golden bench
    When I teach five new questions
    And I run the complete golden bench
    Then no query of the bench changes template
    And no query of the bench changes rows

  @fast
  Scenario: F06-07 The cold-start cost is paid once and is declared
    Given La Roca has just been initialized and has never answered a query
    When I run "roca query 'cuantas memorias hay' --json --no-llm"
    Then the command exits with code 0
    And the JSON output has "latency_ms" less than 5000
    And the output does not mix warm-up noise with the answer
    When I run "roca query 'cuantas memorias hay' --json --no-llm" a second time
    Then the JSON output has "latency_ms" less than 250
