package provider

import (
	"encoding/json"
	"errors"
	"sync"

	"midgard/internal/artifact"
)

// TraceRecorder preserves every native event as JSONL. It validates monotonic
// provider sequence but does not interpret events or mutate kernel state.
type TraceRecorder struct {
	mu       sync.Mutex
	writer   *artifact.Writer
	last     int64
	closed   bool
	artifact artifact.Artifact
}

func NewTraceRecorder(store *artifact.Store) (*TraceRecorder, error) {
	return NewTraceRecorderAfter(store, 0)
}

// NewTraceRecorderAfter starts a trace artifact after an already-recorded
// native sequence. It is used when the request prefix was sealed durably before
// provider I/O and response observations continue in a second artifact.
func NewTraceRecorderAfter(store *artifact.Store, sequence int64) (*TraceRecorder, error) {
	if sequence < 0 {
		return nil, errors.New("provider sequence cannot be negative")
	}
	writer, err := store.NewWriter()
	if err != nil {
		return nil, err
	}
	return &TraceRecorder{writer: writer, last: sequence}, nil
}

func (r *TraceRecorder) Emit(event Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("provider trace is closed")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Sequence != r.last+1 {
		return errors.New("provider sequence is not contiguous")
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if _, err := r.writer.Write(raw); err != nil {
		return err
	}
	r.last = event.Sequence
	return nil
}

func (r *TraceRecorder) Seal() (artifact.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		if r.artifact.Ref == "" {
			return artifact.Artifact{}, errors.New("provider trace was aborted")
		}
		return r.artifact, nil
	}
	r.closed = true
	sealed, err := r.writer.Seal()
	if err != nil {
		return artifact.Artifact{}, err
	}
	r.artifact = sealed
	return sealed, nil
}

func (r *TraceRecorder) Abort() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.writer.Abort()
}
