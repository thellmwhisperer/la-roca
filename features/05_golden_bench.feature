# language: en

@query @golden
Feature: Golden query bench
  As the person responsible for La Roca still answering well after every change
  I want a frozen set of questions with their correct answer declared
  so that a relevance regression shows up before it reaches a real machine.

  Background:
    Given La Roca is installed and initialized
    And a HOME with the seeded world "golden-corpus"
    And the corpus ingest has finished without errors

  @fast
  Scenario Outline: G-aggregates The answer is exactly the true value
    When I run "roca query '<query>' --json --no-llm"
    Then the command exits with code 0
    And the JSON output has "path" equal to "compiler"
    And the JSON output has "queryplan" naming the template "<template>"
    And the returned value equals that of the reference query "<reference>"
    And the JSON output has "latency_ms" less than 250

    Examples:
      | query                                      | template                      | reference                                             |
      | cuantas memorias hay                       | count_memories                | SELECT COUNT(*) FROM memories WHERE supersedes IS NULL |
      | how many memories are there                | count_memories                | SELECT COUNT(*) FROM memories WHERE supersedes IS NULL |
      | count memories by project                  | count_memories_by_project     | ref/count_memories_by_project.sql                     |
      | memorias por proyecto                      | count_memories_by_project     | ref/count_memories_by_project.sql                     |
      | count memories by source agent             | count_memories_by_source_agent| ref/count_memories_by_source_agent.sql                |
      | how many exchanges                         | count_exchanges_total         | SELECT COUNT(*) FROM exchanges                        |
      | total exchanges                            | count_exchanges_total         | SELECT COUNT(*) FROM exchanges                        |
      | how many thinking blocks                   | count_thinking_blocks         | SELECT COUNT(*) FROM thinking_blocks                  |
      | how many tools did the agent use in total  | count_tool_uses_total         | SELECT COUNT(*) FROM tool_uses                        |
      | distinct memory projects                   | distinct_memory_projects      | ref/distinct_memory_projects.sql                      |
      | memory count grouped by project            | count_memories_by_project     | ref/count_memories_by_project.sql                     |

  @fast
  Scenario Outline: G-parity The same question in two languages compiles the same
    When I run "roca query '<query_es>' --json --no-llm"
    And I run "roca query '<query_en>' --json --no-llm"
    Then both outputs have the same "queryplan"
    And both outputs have the same rows

    Examples:
      | query_es                           | query_en                        |
      | memorias por proyecto              | count memories by project       |
      | que feedback tenemos               | show me the latest feedback     |
      | que herramientas uso mas el agente | top used tools                  |

  @fast
  Scenario Outline: G-recall The seeded entry appears in the answer
    When I run "roca query '<query>' --json --no-llm"
    Then the command exits with code 0
    And one of the first <n> rows contains the sentinel "<sentinel>"

    Examples:
      | query                           | n | sentinel                      |
      | guiones largos                  | 1 | guiones largos                |
      | cual es el codigo de lanzamiento| 5 | ROCAE2E_TRINI_ALPHA_7741      |
      | ultimos feedback                | 3 | CENTINELA_FEEDBACK_DORADO     |

  @fast
  Scenario Outline: G-refusal The question is refused with a reason, not guessed
    When I run "roca query '<query>' --json"
    Then the command exits with a code other than 0
    And the output contains "<reason>"
    And no row has been returned
    And the memory count has not changed

    Examples:
      | query                     | reason       |
      | borra todas las memorias  | out of scope |
      | delete all memories       | out of scope |
      | hola                      | out of scope |
      | escribe un poema          | out of scope |
      | sesiones                  | ambiguous    |

  @fast
  Scenario: G-empty A question from a foreign domain returns nothing, inventing nothing
    When I run "roca query 'que tiempo hace en madrid' --json --no-llm"
    Then the command exits with code 0
    And the JSON output has zero rows
    And the JSON output declares the match was empty

  @fast @red-today
  Scenario Outline: G-nocontradiction A filter is not resolved with the aggregate
    When I run "roca query '<query>' --json --no-llm"
    Then the answer is not equal to the unfiltered total
    And the emitted SQL contains a filter by "<term>"

    Examples:
      | query                                                                 | term    |
      | cuantas memorias mencionan Go                                         | Go      |
      | how many memories mention Go                                          | Go      |
      | cuantos agentes distintos han guardado una memoria que mencione binario| binario |

  @acceptance @slow
  Scenario Outline: G-model The model route answers well and on time
    When I run "roca query '<query>' --json"
    Then the command exits with code 0
    And the JSON output has "path" equal to "llm_fallback"
    And the JSON output does not have "degraded"
    And the JSON output has "latency_ms" less than 30000
    And some row contains the sentinel "<sentinel>"

    Examples:
      | query                                             | sentinel                 |
      | what files did I edit recently?                   | worldproj                |
      | que decisiones se tomaron sobre el formato        | ROCAE2E_TRINI_ALPHA_7741 |

  @acceptance @slow
  Scenario: G-coverage The whole bench runs and is reported in aggregate
    When I run the complete golden query bench
    Then every query has a verdict of its own
    And the aggregate report declares how many went by the fast route and how many by the model
    And the report declares the 95th percentile latency of each route
    And no query of the bench is left unrun
