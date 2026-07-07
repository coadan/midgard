package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

type Provider interface {
	ID() string
	Stream(ctx context.Context, packet Packet, emit func(Delta) error) (Usage, error)
}

type ProviderIdentity interface {
	ID() string
}

type Delta struct {
	Text string
}

func ProviderFingerprint(provider ProviderIdentity, modelID string) string {
	id := ""
	if provider != nil {
		id = provider.ID()
	}
	sum := sha256.Sum256([]byte(id + "\n" + modelID))
	return "sha256:" + hex.EncodeToString(sum[:])
}
