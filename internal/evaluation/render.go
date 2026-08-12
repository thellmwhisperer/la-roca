package evaluation

import (
	"fmt"
	"strings"
)

func RenderHuman(report Report) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Retrieval evaluation — %s\n", report.Mode)
	fmt.Fprintf(&out, "fixture: %s\n", report.Fixture)
	fmt.Fprintf(&out, "plan producers: %s\n", producerLine(report.Producers))
	fmt.Fprintf(&out, "cases: %d · passed: %d\n", report.Metrics.Cases, report.Metrics.Passed)
	fmt.Fprintf(&out, "hit@1: %.1f%% (%d/%d)\n", report.Metrics.HitAt1Rate*100,
		report.Metrics.HitAt1, report.Metrics.Cases)
	fmt.Fprintf(&out, "hit@5: %.1f%% (%d/%d)\n", report.Metrics.HitAt5Rate*100,
		report.Metrics.HitAt5, report.Metrics.Cases)
	fmt.Fprintf(&out, "zero-result rate: %.1f%% (%d/%d queries)\n",
		report.Metrics.ZeroResultRate*100, report.Metrics.ZeroResultQueries,
		report.Metrics.TotalQueries)
	fmt.Fprintf(&out, "queries-to-answer: %.2f across %d rescue cases\n",
		report.Metrics.QueriesToAnswer, report.Metrics.RescueCases)
	fmt.Fprintf(&out, "total wall time: %d ms\n", report.TotalWallMS)
	for _, result := range report.Cases {
		status := "PASS"
		if !result.HitAt5 {
			status = "MISS"
			if result.Headroom != "" {
				status = "HEADROOM"
			}
		}
		fmt.Fprintf(&out, "%s %s · hit@1=%t hit@5=%t queries=%d wall=%d ms\n",
			status, result.ID, result.HitAt1, result.HitAt5, result.Queries, result.WallMS)
	}
	return strings.TrimRight(out.String(), "\n")
}

func RenderMarkdown(report Report) string {
	var out strings.Builder
	fmt.Fprintf(&out, "### Retrieval evaluation (%s)\n\n", report.Mode)
	fmt.Fprintf(&out, "Synthetic fixture `%s`; plans produced by %s.\n\n",
		report.Fixture, producerMarkdown(report.Producers))
	out.WriteString("| Metric | Result |\n| --- | ---: |\n")
	fmt.Fprintf(&out, "| hit@1 | %.1f%% (%d/%d) |\n", report.Metrics.HitAt1Rate*100,
		report.Metrics.HitAt1, report.Metrics.Cases)
	fmt.Fprintf(&out, "| hit@5 | %.1f%% (%d/%d) |\n", report.Metrics.HitAt5Rate*100,
		report.Metrics.HitAt5, report.Metrics.Cases)
	fmt.Fprintf(&out, "| zero-result rate | %.1f%% (%d/%d queries) |\n",
		report.Metrics.ZeroResultRate*100, report.Metrics.ZeroResultQueries,
		report.Metrics.TotalQueries)
	fmt.Fprintf(&out, "| queries-to-answer | %.2f (%d rescue cases) |\n",
		report.Metrics.QueriesToAnswer, report.Metrics.RescueCases)
	fmt.Fprintf(&out, "| wall time | %d ms |\n", report.TotalWallMS)
	return strings.TrimRight(out.String(), "\n")
}

func producerLine(producers []Producer) string {
	parts := make([]string, 0, len(producers))
	for _, producer := range producers {
		parts = append(parts, fmt.Sprintf("%s/%s (%d)", producer.Provider, producer.Model, producer.Plans))
	}
	return strings.Join(parts, ", ")
}

func producerMarkdown(producers []Producer) string {
	parts := make([]string, 0, len(producers))
	for _, producer := range producers {
		parts = append(parts, fmt.Sprintf("`%s/%s` (%d plans)",
			producer.Provider, producer.Model, producer.Plans))
	}
	return strings.Join(parts, ", ")
}
