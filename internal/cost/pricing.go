package cost

type Pricing struct {
	ID                   string
	ProviderID           string
	ModelID              string
	InputUSDPerMillion   float64
	OutputUSDPerMillion  float64
	Currency             string
	Source               string
	MissingPricingCaveat string
}
