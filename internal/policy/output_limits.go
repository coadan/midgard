package policy

import "time"

type OutputLimits struct {
	MaxStdoutBytes int64
	MaxStderrBytes int64
	Timeout        time.Duration
}

func DefaultOutputLimits() OutputLimits {
	return OutputLimits{
		MaxStdoutBytes: 64 << 10,
		MaxStderrBytes: 64 << 10,
		Timeout:        30 * time.Second,
	}
}
