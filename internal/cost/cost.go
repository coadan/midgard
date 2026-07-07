package cost

import "midgard/internal/model"

type Result struct {
	PricingID    string
	ProviderID   string
	ModelID      string
	Currency     string
	InputTokens  int64
	OutputTokens int64
	AmountUSD    float64
	Caveat       string
}

func Compute(usage model.Usage, pricing Pricing) Result {
	currency := pricing.Currency
	if currency == "" {
		currency = "USD"
	}
	amount := (float64(usage.InputTokens)/1_000_000)*pricing.InputUSDPerMillion +
		(float64(usage.OutputTokens)/1_000_000)*pricing.OutputUSDPerMillion
	caveat := usage.Caveat
	if pricing.MissingPricingCaveat != "" {
		if caveat != "" {
			caveat += "; "
		}
		caveat += pricing.MissingPricingCaveat
	}
	return Result{
		PricingID:    pricing.ID,
		ProviderID:   usage.ProviderID,
		ModelID:      usage.ModelID,
		Currency:     currency,
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		AmountUSD:    amount,
		Caveat:       caveat,
	}
}
