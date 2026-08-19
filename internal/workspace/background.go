package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxBackgroundJobs = 16

// BackgroundJobs owns host processes for one attached Midgard runtime. Jobs
// are intentionally not detached: closing the runtime terminates every process
// it still owns.
type BackgroundJobs struct {
	mu   sync.Mutex
	jobs map[string]*backgroundJob
}

type backgroundJob struct {
	mu            sync.Mutex
	sessionID     string
	repository    string
	command       string
	cmd           *exec.Cmd
	stdout        lockedCappedBuffer
	stderr        lockedCappedBuffer
	stdoutOffset  int
	stderrOffset  int
	secrets       map[string]string
	done          chan struct{}
	status        string
	jobExitCode   int
	stopRequested bool
}

type lockedCappedBuffer struct {
	mu     sync.Mutex
	buffer cappedBuffer
}

func (b *lockedCappedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(value)
}

func (b *lockedCappedBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String(), b.buffer.truncated
}

func (m *BackgroundJobs) Start(ctx context.Context, sessionID, repository, root, command string, environment, secrets map[string]string, limit int) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	if command == "" {
		return Output{}, errors.New("command is required")
	}
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jobs == nil {
		m.jobs = make(map[string]*backgroundJob)
	}
	if len(m.jobs) >= maxBackgroundJobs {
		return Output{}, errors.New("too many background jobs; poll or stop an existing job first")
	}
	jobID := newBackgroundJobID()
	cmd := exec.Command("/bin/sh", "-lc", command)
	cmd.Dir = root
	cmd.Env = commandEnvironment(environment)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	job := &backgroundJob{sessionID: sessionID, repository: repository, command: command, cmd: cmd,
		secrets: secrets, done: make(chan struct{}), status: "running"}
	job.stdout.buffer.limit, job.stderr.buffer.limit = limit, limit
	cmd.Stdout, cmd.Stderr = &job.stdout, &job.stderr
	if err := cmd.Start(); err != nil {
		return Output{}, err
	}
	m.jobs[jobID] = job
	go func() {
		err := cmd.Wait()
		job.mu.Lock()
		defer job.mu.Unlock()
		job.jobExitCode = exitCode(err)
		switch {
		case job.stopRequested:
			job.status = "stopped"
		case err != nil:
			job.status = "failed"
		default:
			job.status = "completed"
		}
		close(job.done)
	}()
	return Output{JobID: jobID, Status: "running", ExitCode: 0}, nil
}

func (m *BackgroundJobs) Poll(sessionID, jobID string) (Output, error) {
	job, err := m.owned(sessionID, jobID)
	if err != nil {
		return Output{}, err
	}
	output := job.snapshot()
	if output.Status != "running" {
		m.mu.Lock()
		delete(m.jobs, jobID)
		m.mu.Unlock()
	}
	return output, nil
}

func (m *BackgroundJobs) Stop(ctx context.Context, sessionID, jobID string) (Output, error) {
	job, err := m.owned(sessionID, jobID)
	if err != nil {
		return Output{}, err
	}
	job.mu.Lock()
	job.stopRequested = true
	process := job.cmd.Process
	job.mu.Unlock()
	if process != nil {
		_ = syscall.Kill(-process.Pid, syscall.SIGTERM)
	}
	select {
	case <-job.done:
	case <-ctx.Done():
		if process != nil {
			_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
		}
		<-job.done
	case <-time.After(2 * time.Second):
		if process != nil {
			_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
		}
		<-job.done
	}
	output := job.snapshot()
	m.mu.Lock()
	delete(m.jobs, jobID)
	m.mu.Unlock()
	return output, nil
}

func (m *BackgroundJobs) Close() {
	m.mu.Lock()
	jobs := make([]*backgroundJob, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	m.mu.Unlock()
	for _, job := range jobs {
		job.mu.Lock()
		job.stopRequested = true
		process := job.cmd.Process
		job.mu.Unlock()
		if process != nil {
			_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
		}
		<-job.done
	}
	m.mu.Lock()
	clear(m.jobs)
	m.mu.Unlock()
}

func (m *BackgroundJobs) owned(sessionID, jobID string) (*backgroundJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[jobID]
	if job == nil || job.sessionID != sessionID {
		return nil, errors.New("background job is not available in this chat")
	}
	return job, nil
}

func (job *backgroundJob) snapshot() Output {
	job.mu.Lock()
	defer job.mu.Unlock()
	stdoutRaw, stdoutTruncated := job.stdout.snapshot()
	stderrRaw, stderrTruncated := job.stderr.snapshot()
	running := job.status == "running"
	stdout := incrementalRedacted(stdoutRaw, &job.stdoutOffset, running, job.secrets)
	stderr := incrementalRedacted(stderrRaw, &job.stderrOffset, running, job.secrets)
	output := Output{Status: job.status, ExitCode: 0, Truncated: stdoutTruncated || stderrTruncated}
	output.Stdout, output.Stderr = stdout, stderr
	if job.status != "running" {
		exit := job.jobExitCode
		output.JobExitCode = &exit
	}
	return output
}

func incrementalRedacted(raw string, offset *int, running bool, secrets map[string]string) string {
	end := len(raw)
	if running {
		for _, secret := range secrets {
			for length := 1; length < len(secret) && length <= len(raw); length++ {
				if strings.HasSuffix(raw, secret[:length]) && end > len(raw)-length {
					end = len(raw) - length
				}
			}
		}
	}
	if *offset >= end {
		return ""
	}
	chunk := redact(raw[*offset:end], secrets)
	*offset = end
	return chunk
}

func newBackgroundJobID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return "job_" + hex.EncodeToString(value[:])
}
