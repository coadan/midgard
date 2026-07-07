package stream

type Budget struct {
	MaxStreamBytes        int64
	MaxReportBytes        int64
	MaxPayloadBytes       int64
	MaxSinglePayloadBytes int64
	MaxFrames             int
	MaxLineBytes          int
	MaxCommandProposals   int
	MaxRepairAttempts     int
	ProviderMaxTokens     int
}

func DefaultBudget() Budget {
	return Budget{
		MaxStreamBytes:        1 << 20,
		MaxReportBytes:        128 << 10,
		MaxPayloadBytes:       512 << 10,
		MaxSinglePayloadBytes: 256 << 10,
		MaxFrames:             1000,
		MaxLineBytes:          16 << 10,
		MaxCommandProposals:   32,
		MaxRepairAttempts:     2,
		ProviderMaxTokens:     4096,
	}
}
