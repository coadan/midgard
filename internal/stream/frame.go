package stream

import "midgard/internal/artifact"

type FrameType string

const (
	FrameSay        FrameType = "say"
	FrameReport     FrameType = "report"
	FramePayload    FrameType = "payload"
	FrameEdit       FrameType = "edit"
	FrameRef        FrameType = "ref"
	FrameCommand    FrameType = "cmd"
	FrameResult     FrameType = "result"
	FrameError      FrameType = "err"
	FrameReportText FrameType = "report_text"
)

type Frame struct {
	ID        int
	Type      FrameType
	Raw       string
	Line      int
	ByteStart int64
	ByteEnd   int64
	Fields    map[string]string
	Text      string
}

type CommandProposal struct {
	FrameID int
	Repo    string
	Command string
}

type EditIntent struct {
	FrameID int
	File    string
	Action  string
	Mode    string
	Reason  string
	Content string
	Repo    string
	To      string
}

type Ref struct {
	FrameID int
	Kind    string
	Target  string
}

type ResultFrame struct {
	FrameID  int
	Status   string
	Artifact string
	Fields   map[string]string
}

type ParserError struct {
	Code        string
	Message     string
	Line        int
	Recoverable bool
}

type Normalization struct {
	Code    string
	Message string
	Line    int
}

type ParseResult struct {
	Raw               string
	Frames            []Frame
	Artifacts         []artifact.Record
	Commands          []CommandProposal
	Edits             []EditIntent
	Refs              []Ref
	Errors            []ParserError
	Normalizations    []Normalization
	Result            *ResultFrame
	ResultCandidate   map[string]string
	Repair            *RepairPacket
	ProviderMaxTokens int
}
