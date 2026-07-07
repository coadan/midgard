package review

import "midgard/internal/stream"

type Verdict string

const (
	VerdictApproved         Verdict = "approved"
	VerdictChangesRequested Verdict = "changes-requested"
	VerdictBlocked          Verdict = "blocked"
	VerdictFailed           Verdict = "failed"
)

func FromResult(result *stream.ResultFrame) Verdict {
	if result == nil {
		return VerdictFailed
	}
	switch result.Status {
	case "approved":
		return VerdictApproved
	case "changes-requested":
		return VerdictChangesRequested
	case "blocked":
		return VerdictBlocked
	default:
		return VerdictFailed
	}
}
