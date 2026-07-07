package model

import (
	"midgard/internal/stream"
)

type RunResult struct {
	Packet   Packet
	Raw      string
	Parsed   *stream.ParseResult
	Usage    []Usage
	Attempts int
}
