package axi_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestPillsRendersFullContentWithoutTheTableClip(t *testing.T) {
	body := "full pill body " + strings.Repeat("x", 200)
	got := axi.Pills(service.PillList{
		Project: "demo",
		Pills: []service.MemoryRecord{{
			ID: 7, Slug: "build", Project: "demo", CreatedAt: "2026-06-01 00:00:00",
			Content: body,
		}},
		Unslugged: []int64{9},
	})
	if !strings.Contains(got, body) {
		t.Fatalf("pill content was truncated:\n%s", got)
	}
	if strings.Contains(got, "rows[") {
		t.Fatal("pills used the clipped table renderer")
	}
	if !strings.Contains(got, "unslugged") || !strings.Contains(got, "9") {
		t.Fatalf("unslugged ids were omitted:\n%s", got)
	}
}

func TestHandoffsRendersFullContentWithoutTheTableClip(t *testing.T) {
	body := "session close " + strings.Repeat("y", 200)
	got := axi.Handoffs(service.HandoffList{
		Project: "demo",
		Handoffs: []service.MemoryRecord{{
			ID: 3, Project: "demo", CreatedAt: "2026-08-01 00:00:00", Content: body,
		}},
	})
	if !strings.Contains(got, body) {
		t.Fatalf("handoff content was truncated:\n%s", got)
	}
	if strings.Contains(got, "rows[") {
		t.Fatal("handoffs used the clipped table renderer")
	}
}
