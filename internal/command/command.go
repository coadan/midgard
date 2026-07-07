package command

import "time"

type Request struct {
	ID          string
	TaskID      string
	RepoID      string
	Command     string
	CWD         string
	ArtifactDir string
	Env         map[string]string
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
	StdoutTruncated bool
	StderrTruncated bool
	TouchedFiles    []string
	StartedAt       time.Time
	FinishedAt      time.Time
}
