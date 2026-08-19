package eventlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const MaxPayloadBytes = 64 << 10

type Actor string

const (
	ActorUser   Actor = "user"
	ActorModel  Actor = "model"
	ActorServer Actor = "server"
	ActorTool   Actor = "tool"
	ActorPolicy Actor = "policy"
)

type Visibility string

const (
	VisibilityPublic   Visibility = "public"
	VisibilityInternal Visibility = "internal"
	VisibilitySecret   Visibility = "secret"
)

type Event struct {
	EventID       string          `json:"event_id"`
	SessionID     string          `json:"session_id"`
	Sequence      int64           `json:"sequence"`
	TurnID        string          `json:"turn_id,omitempty"`
	Actor         Actor           `json:"actor"`
	Kind          string          `json:"kind"`
	SchemaVersion int             `json:"schema_version"`
	CausationID   string          `json:"causation_id,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Visibility    Visibility      `json:"visibility"`
	Payload       json.RawMessage `json:"payload_json,omitempty"`
	ArtifactRef   string          `json:"artifact_ref,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

type Draft struct {
	EventID       string
	SessionID     string
	TurnID        string
	Actor         Actor
	Kind          string
	SchemaVersion int
	CausationID   string
	CorrelationID string
	Visibility    Visibility
	Payload       json.RawMessage
	ArtifactRef   string
}

var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

func (d Draft) Validate() error {
	if strings.TrimSpace(d.EventID) == "" || strings.TrimSpace(d.SessionID) == "" {
		return errors.New("event_id and session_id are required")
	}
	switch d.Actor {
	case ActorUser, ActorModel, ActorServer, ActorTool, ActorPolicy:
	default:
		return fmt.Errorf("invalid actor %q", d.Actor)
	}
	if !kindPattern.MatchString(d.Kind) {
		return fmt.Errorf("invalid event kind %q", d.Kind)
	}
	if d.SchemaVersion < 1 {
		return errors.New("schema_version must be positive")
	}
	switch d.Visibility {
	case VisibilityPublic, VisibilityInternal, VisibilitySecret:
	default:
		return fmt.Errorf("invalid visibility %q", d.Visibility)
	}
	if len(d.Payload) > MaxPayloadBytes {
		return fmt.Errorf("payload exceeds %d bytes", MaxPayloadBytes)
	}
	if len(d.Payload) > 0 && !json.Valid(d.Payload) {
		return errors.New("payload_json is not valid JSON")
	}
	if d.ArtifactRef != "" && !validArtifactRef(d.ArtifactRef) {
		return errors.New("artifact_ref must be sha256 followed by 64 lowercase hex characters")
	}
	return nil
}

func validArtifactRef(ref string) bool {
	if len(ref) != len("sha256:")+64 || !strings.HasPrefix(ref, "sha256:") {
		return false
	}
	for _, r := range ref[len("sha256:"):] {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
