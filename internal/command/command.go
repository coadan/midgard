package command

import (
	"context"
	"time"
)

type Request struct {
	ID             string
	TaskID         string
	RepoID         string
	Command        string
	CWD            string
	ArtifactDir    string
	ArtifactPrefix string
	Env            map[string]string
	Fence          func(context.Context) error
}

type Result struct {
	ID              string
	TaskID          string
	RepoID          string
	Command         string
	CWD             string
	ExitCode        int
	TimedOut        bool
	StdoutPath      string
	StderrPath      string
	ResultPath      string
	StdoutChecksum  string
	StderrChecksum  string
	ResultChecksum  string
	StdoutTruncated bool
	StderrTruncated bool
	TouchedFiles    []string
	StartedAt       time.Time
	FinishedAt      time.Time
}
