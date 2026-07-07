package model

type Usage struct {
	ProviderID   string
	ModelID      string
	Role         Role
	InputTokens  int64
	OutputTokens int64
	Raw          string
	Caveat       string
}
