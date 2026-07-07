package policy

import "midgard/internal/artifact"

type Decision struct {
	Allowed bool
	Reason  string
}

func CanExecuteArtifact(record artifact.Record) Decision {
	if record.Type != artifact.TypePayload {
		return Decision{Allowed: false, Reason: "artifact is not a payload"}
	}
	if record.State != artifact.StateSealed {
		return Decision{Allowed: false, Reason: "payload is not sealed"}
	}
	return Decision{Allowed: true}
}
