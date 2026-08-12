package evaluation

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// maxChars is the text budget evaluation executes under. The ruler measures
// what retrieval returned, not what a terminal would show, so a marker deep
// inside a long field still counts as found.
const maxChars = 1 << 22

type Plan struct {
	SQL      []string `json:"sql"`
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Degraded string   `json:"degraded,omitempty"`
}

type Planner interface {
	Plan(context.Context, Case, int, string) (Plan, error)
}

type plannerFunc func(context.Context, Case, int, string) (Plan, error)

func (fn plannerFunc) Plan(ctx context.Context, golden Case, attempt int, question string) (Plan, error) {
	return fn(ctx, golden, attempt, question)
}

func ReplayPlanner(suite Suite) Planner {
	return plannerFunc(func(_ context.Context, golden Case, attempt int, _ string) (Plan, error) {
		plan, exists := suite.Plans[golden.ID]
		if !exists || attempt >= len(plan.SQL) {
			return Plan{}, fmt.Errorf("no recorded plan for %s attempt %d", golden.ID, attempt+1)
		}
		return Plan{SQL: []string{plan.SQL[attempt]}, Provider: plan.Provider, Model: plan.Model}, nil
	})
}

func LivePlanner(svc *service.Service) Planner {
	return plannerFunc(func(ctx context.Context, golden Case, _ int, question string) (Plan, error) {
		result, err := svc.Query(ctx, service.QueryRequest{Question: question, SQLOnly: true})
		if err != nil {
			return Plan{}, fmt.Errorf("generate live plan for %s: %w", golden.ID, err)
		}
		if result.Engine == "" || result.Model == "" {
			return Plan{}, fmt.Errorf("generate live plan for %s: no provider/model answered", golden.ID)
		}
		if result.SQL == "" {
			return Plan{}, fmt.Errorf("generate live plan for %s: %s", golden.ID, result.Message)
		}
		return Plan{SQL: []string{result.SQL}, Provider: result.Engine,
			Model: result.Model, Degraded: result.Degraded}, nil
	})
}

type AttemptResult struct {
	Question   string           `json:"question"`
	SQL        string           `json:"sql"`
	Provider   string           `json:"provider"`
	Model      string           `json:"model"`
	Degraded   string           `json:"degraded,omitempty"`
	Rows       int              `json:"rows"`
	Columns    []string         `json:"columns,omitempty"`
	ResultRows []map[string]any `json:"result_rows,omitempty"`
	HitAt1     bool             `json:"hit_at_1"`
	HitAt5     bool             `json:"hit_at_5"`
	WallMS     int64            `json:"wall_ms"`
}

type CaseResult struct {
	ID              string          `json:"id"`
	Category        string          `json:"category"`
	Question        string          `json:"question"`
	ExpectedKind    string          `json:"expected_kind"`
	ExpectedMarker  string          `json:"expected_marker"`
	Headroom        string          `json:"headroom,omitempty"`
	HitAt1          bool            `json:"hit_at_1"`
	HitAt5          bool            `json:"hit_at_5"`
	Queries         int             `json:"queries"`
	QueriesToAnswer int             `json:"queries_to_answer,omitempty"`
	WallMS          int64           `json:"wall_ms"`
	Attempts        []AttemptResult `json:"attempts"`
}

type Metrics struct {
	Cases               int     `json:"cases"`
	Passed              int     `json:"passed"`
	HitAt1              int     `json:"hit_at_1"`
	HitAt5              int     `json:"hit_at_5"`
	HitAt1Rate          float64 `json:"hit_at_1_rate"`
	HitAt5Rate          float64 `json:"hit_at_5_rate"`
	TotalQueries        int     `json:"total_queries"`
	ZeroResultQueries   int     `json:"zero_result_queries"`
	ZeroResultRate      float64 `json:"zero_result_rate"`
	RescueCases         int     `json:"rescue_cases"`
	AnsweredRescueCases int     `json:"answered_rescue_cases"`
	QueriesToAnswer     float64 `json:"queries_to_answer"`
}

type Producer struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Plans    int    `json:"plans"`
}

type Report struct {
	SchemaVersion int          `json:"schema_version"`
	Mode          string       `json:"mode"`
	Fixture       string       `json:"fixture"`
	Producers     []Producer   `json:"plan_producers"`
	Metrics       Metrics      `json:"metrics"`
	Cases         []CaseResult `json:"cases"`
	TotalWallMS   int64        `json:"total_wall_ms"`
	LogPath       string       `json:"log_path"`
}

func Run(ctx context.Context, svc *service.Service, suite Suite, planner Planner, mode string) (Report, error) {
	started := time.Now()
	report := Report{SchemaVersion: 1, Mode: mode, Fixture: suite.Fixture}
	producerCounts := map[string]int{}
	qtaTotal := 0
	for _, golden := range suite.Cases {
		result, err := runCase(ctx, svc, planner, golden, producerCounts, &report.Metrics)
		if err != nil {
			return report, err
		}
		report.Cases = append(report.Cases, result)
		if result.HitAt1 {
			report.Metrics.HitAt1++
		}
		if result.HitAt5 {
			report.Metrics.HitAt5++
			report.Metrics.Passed++
		}
		if len(golden.RescuePath) > 0 {
			report.Metrics.RescueCases++
			if result.QueriesToAnswer > 0 {
				qtaTotal += result.QueriesToAnswer
				report.Metrics.AnsweredRescueCases++
			}
		}
	}
	report.Metrics.Cases = len(report.Cases)
	report.Metrics.HitAt1Rate = ratio(report.Metrics.HitAt1, report.Metrics.Cases)
	report.Metrics.HitAt5Rate = ratio(report.Metrics.HitAt5, report.Metrics.Cases)
	report.Metrics.ZeroResultRate = ratio(report.Metrics.ZeroResultQueries, report.Metrics.TotalQueries)
	if report.Metrics.AnsweredRescueCases > 0 {
		report.Metrics.QueriesToAnswer = float64(qtaTotal) /
			float64(report.Metrics.AnsweredRescueCases)
	}
	report.Producers = producers(producerCounts)
	report.TotalWallMS = time.Since(started).Milliseconds()
	return report, nil
}

// runCase walks the question and then its declared rescue path. hit@1 belongs
// to the first question alone: a rescue attempt that lands the marker on the
// top row is reported by queries-to-answer, so the top-1 ruler keeps meaning
// the same thing across prompt, query, and rescue-path revisions.
func runCase(ctx context.Context, svc *service.Service, planner Planner, golden Case,
	producerCounts map[string]int, metrics *Metrics) (CaseResult, error) {
	started := time.Now()
	result := CaseResult{ID: golden.ID, Category: golden.Category, Question: golden.Question,
		ExpectedKind: golden.ExpectedKind, ExpectedMarker: golden.ExpectedMarker,
		Headroom: golden.Headroom}
	questions := append([]string{golden.Question}, golden.RescuePath...)
	for attempt, question := range questions {
		attemptStarted := time.Now()
		plan, err := planner.Plan(ctx, golden, attempt, question)
		if err != nil {
			return result, err
		}
		if len(plan.SQL) != 1 {
			return result, fmt.Errorf("planner returned %d statements for %s attempt %d",
				len(plan.SQL), golden.ID, attempt+1)
		}
		executed, err := svc.Exec(ctx, service.ExecRequest{SQL: plan.SQL[0], MaxChars: maxChars})
		if err != nil {
			return result, fmt.Errorf("execute %s attempt %d: %w", golden.ID, attempt+1, err)
		}
		at1, at5, err := Match(golden, executed.Rows)
		if err != nil {
			return result, fmt.Errorf("match %s: %w", golden.ID, err)
		}
		metrics.TotalQueries++
		if executed.RowCount == 0 {
			metrics.ZeroResultQueries++
		}
		producerCounts[plan.Provider+"\x00"+plan.Model]++
		result.Attempts = append(result.Attempts, AttemptResult{
			Question: question, SQL: executed.SQL, Provider: plan.Provider, Model: plan.Model,
			Degraded: plan.Degraded, Rows: executed.RowCount, Columns: executed.Columns,
			ResultRows: executed.Rows, HitAt1: at1, HitAt5: at5,
			WallMS: time.Since(attemptStarted).Milliseconds(),
		})
		result.Queries, result.HitAt5 = attempt+1, at5
		if attempt == 0 {
			result.HitAt1 = at1
		}
		if at5 {
			result.QueriesToAnswer = attempt + 1
			break
		}
	}
	result.WallMS = time.Since(started).Milliseconds()
	return result, nil
}

func Match(golden Case, rows []map[string]any) (bool, bool, error) {
	match, err := matcher(golden)
	if err != nil {
		return false, false, err
	}
	return match(rows, 1), match(rows, 5), nil
}

func matcher(golden Case) (func([]map[string]any, int) bool, error) {
	switch golden.ExpectedKind {
	case "row_contains":
		marker := strings.ToLower(golden.ExpectedMarker)
		return func(rows []map[string]any, limit int) bool {
			return anyRow(rows, limit, func(row map[string]any) bool {
				for _, value := range row {
					if strings.Contains(strings.ToLower(fmt.Sprint(value)), marker) {
						return true
					}
				}
				return false
			})
		}, nil
	case "field_equals":
		field, value, ok := strings.Cut(golden.ExpectedMarker, "=")
		if !ok || field == "" {
			return nil, fmt.Errorf("field_equals marker must be field=value")
		}
		return func(rows []map[string]any, limit int) bool {
			return anyRow(rows, limit, func(row map[string]any) bool {
				return fmt.Sprint(row[field]) == value
			})
		}, nil
	case "count_gt", "count_equals":
		expected, err := strconv.ParseFloat(golden.ExpectedMarker, 64)
		if err != nil {
			return nil, fmt.Errorf("%s marker is not numeric: %w", golden.ExpectedKind, err)
		}
		return func(rows []map[string]any, limit int) bool {
			return anyRow(rows, limit, func(row map[string]any) bool {
				for field, value := range row {
					name := strings.ToLower(field)
					if name != "count" && !strings.Contains(name, "count(") {
						continue
					}
					actual, ok := number(value)
					if ok && (golden.ExpectedKind == "count_gt" && actual > expected ||
						golden.ExpectedKind == "count_equals" && actual == expected) {
						return true
					}
				}
				return false
			})
		}, nil
	default:
		return nil, fmt.Errorf("unknown expected_kind %q", golden.ExpectedKind)
	}
}

func anyRow(rows []map[string]any, limit int, match func(map[string]any) bool) bool {
	if len(rows) < limit {
		limit = len(rows)
	}
	for _, row := range rows[:limit] {
		if match(row) {
			return true
		}
	}
	return false
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func producers(counts map[string]int) []Producer {
	result := make([]Producer, 0, len(counts))
	for key, count := range counts {
		provider, model, _ := strings.Cut(key, "\x00")
		result = append(result, Producer{Provider: provider, Model: model, Plans: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Provider == result[j].Provider {
			return result[i].Model < result[j].Model
		}
		return result[i].Provider < result[j].Provider
	})
	return result
}
