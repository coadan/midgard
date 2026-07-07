package cost

import (
	"math"
	"testing"

	"midgard/internal/model"
)

func TestComputeCostFromObservedUsage(t *testing.T) {
	got := Compute(model.Usage{
		ProviderID:   "fake",
		ModelID:      "fake-model",
		InputTokens:  1_000_000,
		OutputTokens: 500_000,
	}, Pricing{
		ID:                  "snapshot",
		InputUSDPerMillion:  0.20,
		OutputUSDPerMillion: 0.80,
		Currency:            "USD",
	})
	if math.Abs(got.AmountUSD-0.60) > 0.000001 {
		t.Fatalf("amount = %f, want 0.60", got.AmountUSD)
	}
	if got.PricingID != "snapshot" {
		t.Fatalf("pricing id = %s", got.PricingID)
	}
}
