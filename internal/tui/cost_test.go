package tui

import (
	"strings"
	"testing"

	"midgard/internal/session"
)

func TestRenderTurnCostUsesDeepSeekV4ProCachePricing(t *testing.T) {
	got := renderTurnCost(session.TurnUsage{
		Model: "deepseek-v4-pro", InputTokens: 12_000,
		CacheHitInputTokens: 8_000, CacheMissInputTokens: 4_000, OutputTokens: 2_000,
		ThinkingTokens: 750, ProviderDurationMillis: 500,
		PeakContextTokens: 72_000, ContextLimitTokens: 128_000, Compactions: 1,
	})
	if got != "≈ $0.003509 · ↻ 67% cache · ↑ 12.0k input · ↓ 2.0k output · ◇ 0.8k thinking · 4000.0 tok/s · ◫ 56% context · 1 compacted" {
		t.Fatalf("cost metadata = %q", got)
	}
}

func TestRenderTurnCostOmitsEstimateForUnknownModel(t *testing.T) {
	got := renderTurnCost(session.TurnUsage{Model: "custom", InputTokens: 100, CacheMissInputTokens: 100, OutputTokens: 20})
	if strings.Contains(got, "$") || got != "↻ 0% cache · ↑ 0.1k input · ↓ 20 output" {
		t.Fatalf("unknown model metadata = %q", got)
	}
}
