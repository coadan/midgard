package tui

import (
	"fmt"
	"strings"
	"time"

	"midgard/internal/session"
)

type tokenPrices struct {
	cacheHitInput  float64
	cacheMissInput float64
	output         float64
}

// Prices are USD per million tokens from DeepSeek's public pricing table,
// verified 2026-08-14. Unknown models deliberately show usage without a cost.
var deepSeekPrices = map[string]tokenPrices{
	"deepseek-v4-flash": {cacheHitInput: 0.0028, cacheMissInput: 0.14, output: 0.28},
	"deepseek-v4-pro":   {cacheHitInput: 0.003625, cacheMissInput: 0.435, output: 0.87},
}

func renderTurnCost(usage session.TurnUsage) string {
	parts := make([]string, 0, 3)
	if prices, ok := deepSeekPrices[usage.Model]; ok {
		cost := (float64(usage.CacheHitInputTokens)*prices.cacheHitInput +
			float64(usage.CacheMissInputTokens)*prices.cacheMissInput +
			float64(usage.OutputTokens)*prices.output) / 1_000_000
		parts = append(parts, "≈ "+formatUSD(cost))
	}
	parts = append(parts,
		"↻ "+formatCacheRate(usage.CacheHitInputTokens, usage.InputTokens)+" cache",
		"↑ "+formatTokens(usage.InputTokens)+" input",
		"↓ "+formatTokens(usage.OutputTokens)+" output")
	if usage.ThinkingTokens > 0 {
		parts = append(parts, "◇ "+formatTokens(usage.ThinkingTokens)+" thinking")
	}
	if usage.ProviderDurationMillis > 0 {
		parts = append(parts, formatThroughput(usage.OutputTokens, time.Duration(usage.ProviderDurationMillis)*time.Millisecond))
	}
	if usage.ContextLimitTokens > 0 {
		context := "◫ " + formatContextRate(usage.PeakContextTokens, usage.ContextLimitTokens) + " context"
		if usage.Compactions > 0 {
			context += fmt.Sprintf(" · %d compacted", usage.Compactions)
		}
		parts = append(parts, context)
	}
	return strings.Join(parts, " · ")
}

func renderStyledTurnCost(usage session.TurnUsage) string {
	plain := renderTurnCost(usage)
	cached := "↻ " + formatCacheRate(usage.CacheHitInputTokens, usage.InputTokens) + " cache"
	parts := strings.SplitN(plain, cached, 2)
	if len(parts) != 2 {
		return colors.Muted.Render(plain)
	}
	return colors.Muted.Render(parts[0]) + colors.Subtle.Render(cached) + colors.Muted.Render(parts[1])
}

func (m Model) providerStatus(label string) string {
	status := fmt.Sprintf("%s · call %d/%d", label, m.providerCalls, m.maxCalls)
	if m.contextLimitTokens > 0 {
		status += " · ◫ " + formatContextRate(m.contextTokens, m.contextLimitTokens) + " context"
	}
	if m.inputTokens == 0 && m.outputTokens == 0 {
		return status
	}
	return status + " · ↻ " + formatCacheRate(m.cacheHitInputTokens, m.inputTokens) + " cache · ↑ " +
		formatTokens(m.inputTokens) + " input · ↓ " + formatTokens(m.outputTokens) + " output" + m.liveGenerationStats()
}

func (m Model) renderCurrentStatus() string {
	label := ""
	switch {
	case strings.HasPrefix(m.status, "waiting for the model"):
		label = "waiting for the model"
	case strings.HasPrefix(m.status, "thinking"):
		label = "thinking"
	}
	if label == "" {
		return styledStatus(m.status)
	}
	status := colors.Warning.Render("●") + " " + colors.Section.Render(label) +
		"   " + colors.Muted.Render(fmt.Sprintf("%d/%d", m.providerCalls, m.maxCalls))
	if m.contextLimitTokens > 0 {
		status += "   " + colors.Location.Render("◫ "+formatContextRate(m.contextTokens, m.contextLimitTokens))
	}
	if m.inputTokens == 0 && m.outputTokens == 0 {
		return status
	}
	return status + "   " + colors.Subtle.Render("↻ "+formatCacheRate(m.cacheHitInputTokens, m.inputTokens)) +
		"   " + colors.Accent.Render("↑ "+formatTokens(m.inputTokens)) +
		"   " + colors.Brand.Render("↓ "+formatTokens(m.outputTokens)) + colors.Muted.Render(m.liveGenerationStats())
}

func (m Model) liveGenerationStats() string {
	stats := ""
	if m.thinkingTokens > 0 {
		stats += " · ◇ " + formatTokens(m.thinkingTokens) + " thinking"
	}
	if m.providerDuration > 0 {
		stats += " · " + formatThroughput(m.outputTokens, m.providerDuration)
	}
	return stats
}

func formatThroughput(tokens int64, duration time.Duration) string {
	if tokens <= 0 || duration <= 0 {
		return "0 tok/s"
	}
	return fmt.Sprintf("%.1f tok/s", float64(tokens)/duration.Seconds())
}

func formatContextRate(used, limit int64) string {
	if limit <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(used)/float64(limit))
}

func formatCacheRate(hit, total int64) string {
	if total <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(hit)/float64(total))
}

func formatUSD(value float64) string {
	switch {
	case value >= 1:
		return fmt.Sprintf("$%.2f", value)
	case value >= 0.01:
		return fmt.Sprintf("$%.4f", value)
	default:
		return fmt.Sprintf("$%.6f", value)
	}
}

func formatTokens(value int64) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(value)/1_000_000)
	case value >= 100:
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}
