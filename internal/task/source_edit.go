package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"midgard/internal/artifact"
	"midgard/internal/gitrepo"
	"midgard/internal/model"
	"midgard/internal/state"
	"midgard/internal/stream"
)

type sourceEditAppliedEvent struct {
	FrameID         int    `json:"frame_id"`
	Role            string `json:"role"`
	RepoID          string `json:"repo_id"`
	File            string `json:"file"`
	Action          string `json:"action"`
	Mode            string `json:"mode"`
	Reason          string `json:"reason"`
	Content         string `json:"content"`
	Strategy        string `json:"strategy"`
	BeforePorcelain string `json:"before_porcelain"`
	AfterPorcelain  string `json:"after_porcelain"`
}

type sourceEditNormalizedEvent struct {
	FrameID         int    `json:"frame_id"`
	Role            string `json:"role"`
	RepoID          string `json:"repo_id"`
	File            string `json:"file"`
	Content         string `json:"content"`
	Strategy        string `json:"strategy"`
	OriginalError   string `json:"original_error"`
	BeforeChecksum  string `json:"before_checksum"`
	AfterChecksum   string `json:"after_checksum"`
	RemovedChecksum string `json:"removed_checksum"`
	AddedChecksum   string `json:"added_checksum"`
	RemovedBytes    int    `json:"removed_bytes"`
	AddedBytes      int    `json:"added_bytes"`
}

type sourceEditApplyFailedEvent struct {
	FrameID               int    `json:"frame_id"`
	Role                  string `json:"role"`
	RepoID                string `json:"repo_id"`
	File                  string `json:"file"`
	Action                string `json:"action"`
	Mode                  string `json:"mode"`
	Reason                string `json:"reason"`
	Content               string `json:"content"`
	Attempt               int    `json:"attempt"`
	MaxRepairs            int    `json:"max_repairs"`
	RemainingRepairs      int    `json:"remaining_repairs"`
	Error                 string `json:"error"`
	Stderr                string `json:"stderr"`
	BeforePorcelain       string `json:"before_porcelain"`
	PartialApplied        bool   `json:"partial_applied"`
	FallbackError         string `json:"fallback_error,omitempty"`
	FailedPatchArtifact   string `json:"failed_patch_artifact"`
	StderrArtifact        string `json:"stderr_artifact"`
	SourceContextArtifact string `json:"source_context_artifact"`
}

type sourceEditApplyFailure struct {
	event sourceEditApplyFailedEvent
}

func (f *sourceEditApplyFailure) Error() string {
	if f == nil {
		return ""
	}
	return fmt.Sprintf("source edit patch apply failed for %s: %s", f.event.File, f.event.Error)
}

func (f *sourceEditApplyFailure) modelFailure(store artifact.Store) model.SourceEditApplyFailure {
	if f == nil {
		return model.SourceEditApplyFailure{}
	}
	return model.SourceEditApplyFailure{
		Attempt:               f.event.Attempt,
		RemainingAttempts:     f.event.RemainingRepairs,
		File:                  f.event.File,
		Repo:                  f.event.RepoID,
		Action:                f.event.Action,
		Reason:                f.event.Reason,
		ContentArtifact:       f.event.Content,
		FailedPatchArtifact:   artifactRef(f.event.FailedPatchArtifact),
		StderrArtifact:        artifactRef(f.event.StderrArtifact),
		SourceContextArtifact: artifactRef(f.event.SourceContextArtifact),
		FailedPatchPreview:    artifactPreview(store, f.event.FailedPatchArtifact, 4096),
		StderrPreview:         firstNonBlank(f.event.Stderr, artifactPreview(store, f.event.StderrArtifact, 2048)),
		SourceContextPreview:  artifactPreview(store, f.event.SourceContextArtifact, 12000),
		PartialApplied:        f.event.PartialApplied,
		Error:                 f.event.Error,
	}
}

func applySourceEdits(ctx context.Context, db *state.DB, taskID string, role model.Role, worktrees []WorktreeStatus, store artifact.Store, parsed *stream.ParseResult, attempt, maxRepairs int) (*sourceEditApplyFailure, error) {
	if err := CheckExecution(ctx); err != nil {
		return nil, err
	}
	if parsed == nil || role != model.RoleImplementer {
		return nil, nil
	}
	appliedPatchArtifacts := map[string]bool{}
	for _, edit := range parsed.Edits {
		if err := CheckExecution(ctx); err != nil {
			return nil, err
		}
		if edit.Mode != "patch" {
			continue
		}
		contentPath, err := editContentPath(edit)
		if err != nil {
			return nil, err
		}
		key := edit.Repo + "\x00" + contentPath
		if appliedPatchArtifacts[key] {
			continue
		}
		appliedPatchArtifacts[key] = true
		failure, err := applyPatchEdit(ctx, db, taskID, role, worktrees, store, parsed.Artifacts, edit, attempt, maxRepairs)
		if err != nil || failure != nil {
			return failure, err
		}
	}
	return nil, nil
}

func applyPatchEdit(ctx context.Context, db *state.DB, taskID string, role model.Role, worktrees []WorktreeStatus, store artifact.Store, artifacts []artifact.Record, edit stream.EditIntent, attempt, maxRepairs int) (*sourceEditApplyFailure, error) {
	wt, err := worktreeForRepo(worktrees, edit.Repo)
	if err != nil {
		return nil, err
	}
	contentPath, err := editContentPath(edit)
	if err != nil {
		return nil, err
	}
	rec, ok := artifactByPath(artifacts, contentPath)
	if !ok {
		return nil, fmt.Errorf("patch edit content artifact %q not found", contentPath)
	}
	if rec.Type != artifact.TypePayload || rec.State != artifact.StateSealed || rec.PayloadType != "patch" {
		return nil, fmt.Errorf("patch edit content artifact %q is not a sealed patch payload", contentPath)
	}
	patch, err := store.Read(contentPath)
	if err != nil {
		return nil, err
	}
	patch = sanitizePatchPayload(patch)
	if err := CheckExecution(ctx); err != nil {
		return nil, err
	}
	before, _ := gitrepo.WorktreeStatus(ctx, wt.Path)
	if applyErr := gitrepo.ApplyPatchWithRejects(ctx, wt.Path, patch); applyErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var fallbackErr error
		var typedApplyErr *gitrepo.ApplyPatchError
		switch {
		case edit.Action != "modify":
			fallbackErr = fmt.Errorf("action %q is not an in-place modification", edit.Action)
		case !errors.As(applyErr, &typedApplyErr):
			fallbackErr = fmt.Errorf("git apply error is not structured")
		case typedApplyErr.Partial:
			fallbackErr = fmt.Errorf("failed git apply changed the worktree")
		default:
			if err := CheckExecution(ctx); err != nil {
				return nil, err
			}
			normalized, err := gitrepo.ApplyUniqueReplacement(wt.Path, edit.File, patch)
			if err == nil {
				after, _ := gitrepo.WorktreeStatus(ctx, wt.Path)
				if err := recordSourceEditNormalized(ctx, db, taskID, role, wt, edit, contentPath, applyErr, normalized); err != nil {
					return nil, err
				}
				if err := recordSourceEditApplied(ctx, db, taskID, role, wt, edit, contentPath, "unique-replacement", before.Porcelain, after.Porcelain); err != nil {
					return nil, err
				}
				return nil, nil
			}
			fallbackErr = err
		}
		failure, persistErr := persistSourceEditApplyFailure(ctx, db, taskID, role, wt, store, edit, contentPath, patch, before.Porcelain, applyErr, fallbackErr, attempt, maxRepairs)
		if persistErr != nil {
			return nil, persistErr
		}
		return failure, nil
	}
	after, _ := gitrepo.WorktreeStatus(ctx, wt.Path)
	return nil, recordSourceEditApplied(ctx, db, taskID, role, wt, edit, contentPath, "git-apply", before.Porcelain, after.Porcelain)
}

func recordSourceEditApplied(ctx context.Context, db *state.DB, taskID string, role model.Role, wt WorktreeStatus, edit stream.EditIntent, contentPath, strategy, beforePorcelain, afterPorcelain string) error {
	payload, err := json.Marshal(sourceEditAppliedEvent{
		FrameID:         edit.FrameID,
		Role:            role.String(),
		RepoID:          wt.RepoID,
		File:            edit.File,
		Action:          edit.Action,
		Mode:            edit.Mode,
		Reason:          edit.Reason,
		Content:         "artifact:" + contentPath,
		Strategy:        strategy,
		BeforePorcelain: beforePorcelain,
		AfterPorcelain:  afterPorcelain,
	})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "source_edit.applied", Payload: string(payload)})
	return err
}

func recordSourceEditNormalized(ctx context.Context, db *state.DB, taskID string, role model.Role, wt WorktreeStatus, edit stream.EditIntent, contentPath string, applyErr error, normalized gitrepo.UniqueReplacementResult) error {
	payload, err := json.Marshal(sourceEditNormalizedEvent{
		FrameID:         edit.FrameID,
		Role:            role.String(),
		RepoID:          wt.RepoID,
		File:            edit.File,
		Content:         "artifact:" + contentPath,
		Strategy:        "unique-replacement",
		OriginalError:   applyErr.Error(),
		BeforeChecksum:  normalized.BeforeChecksum,
		AfterChecksum:   normalized.AfterChecksum,
		RemovedChecksum: normalized.RemovedChecksum,
		AddedChecksum:   normalized.AddedChecksum,
		RemovedBytes:    normalized.RemovedBytes,
		AddedBytes:      normalized.AddedBytes,
	})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "source_edit.normalized", Payload: string(payload)})
	return err
}

func persistSourceEditApplyFailure(ctx context.Context, db *state.DB, taskID string, role model.Role, wt WorktreeStatus, store artifact.Store, edit stream.EditIntent, contentPath string, patch []byte, beforePorcelain string, applyErr, fallbackErr error, attempt, maxRepairs int) (*sourceEditApplyFailure, error) {
	stderr := strings.TrimSpace(applyErr.Error())
	var gitApplyErr *gitrepo.ApplyPatchError
	if errors.As(applyErr, &gitApplyErr) && strings.TrimSpace(gitApplyErr.Stderr) != "" {
		stderr = strings.TrimSpace(gitApplyErr.Stderr)
	}
	fallbackMessage := ""
	if fallbackErr != nil {
		fallbackMessage = fallbackErr.Error()
		stderr += "\n\nunique replacement fallback not applied: " + fallbackMessage
	}
	prefix := fmt.Sprintf("source-edits/apply-failures/%d", attempt)
	patchRec, err := putSourceEditDiagnostic(ctx, db, taskID, role, store, artifact.Record{
		Path:         prefix + "/patch.diff",
		Type:         artifact.TypePayload,
		State:        artifact.StateSealed,
		ProducerRole: role.String(),
		PayloadType:  "patch",
	}, patch)
	if err != nil {
		return nil, err
	}
	stderrRec, err := putSourceEditDiagnostic(ctx, db, taskID, role, store, artifact.Record{
		Path:         prefix + "/stderr.txt",
		Type:         artifact.TypePayload,
		State:        artifact.StateSealed,
		ProducerRole: role.String(),
		PayloadType:  "text",
	}, []byte(stderr+"\n"))
	if err != nil {
		return nil, err
	}
	contextRec, err := putSourceEditDiagnostic(ctx, db, taskID, role, store, artifact.Record{
		Path:         prefix + "/source-context.txt",
		Type:         artifact.TypePayload,
		State:        artifact.StateSealed,
		ProducerRole: role.String(),
		PayloadType:  "text",
	}, sourceEditContext(ctx, wt, edit, stderr))
	if err != nil {
		return nil, err
	}
	remainingRepairs := max(0, maxRepairs-attempt+1)
	event := sourceEditApplyFailedEvent{
		FrameID:               edit.FrameID,
		Role:                  role.String(),
		RepoID:                wt.RepoID,
		File:                  edit.File,
		Action:                edit.Action,
		Mode:                  edit.Mode,
		Reason:                edit.Reason,
		Content:               "artifact:" + contentPath,
		Attempt:               attempt,
		MaxRepairs:            maxRepairs,
		RemainingRepairs:      remainingRepairs,
		Error:                 applyErr.Error(),
		Stderr:                stderr,
		BeforePorcelain:       beforePorcelain,
		PartialApplied:        gitApplyErr != nil && gitApplyErr.Partial,
		FallbackError:         fallbackMessage,
		FailedPatchArtifact:   patchRec.Path,
		StderrArtifact:        stderrRec.Path,
		SourceContextArtifact: contextRec.Path,
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if _, err := db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "source_edit.apply_failed", Payload: string(payload)}); err != nil {
		return nil, err
	}
	return &sourceEditApplyFailure{event: event}, nil
}

func putSourceEditDiagnostic(ctx context.Context, db *state.DB, taskID string, role model.Role, store artifact.Store, rec artifact.Record, data []byte) (artifact.Record, error) {
	if err := CheckExecution(ctx); err != nil {
		return artifact.Record{}, err
	}
	stored, err := store.Put(rec, data)
	if err != nil {
		return artifact.Record{}, err
	}
	stateArtifact := state.Artifact{
		ID:           artifactID(taskID, stored.Path),
		TaskID:       taskID,
		Type:         stored.Type,
		Path:         stored.Path,
		Checksum:     stored.Checksum,
		ProducerRole: role.String(),
		State:        stored.State,
	}
	if err := db.InsertArtifact(ctx, stateArtifact); err != nil && !strings.Contains(err.Error(), "constraint failed") {
		return artifact.Record{}, err
	}
	return stored, nil
}

func sourceEditContext(ctx context.Context, wt WorktreeStatus, edit stream.EditIntent, patchStderr string) []byte {
	var b strings.Builder
	b.WriteString("repo:")
	b.WriteString(wt.RepoID)
	b.WriteByte('\n')
	for index, target := range sourceEditContextTargets(edit.File, patchStderr) {
		if index > 0 {
			b.WriteByte('\n')
		}
		writeSourceEditFileContext(ctx, &b, wt.Path, target)
	}
	return []byte(b.String())
}

type sourceEditContextTarget struct {
	Path       string
	Line       int
	Targeted   bool
	Primary    bool
	RejectFile bool
}

func sourceEditContextTargets(editFile, patchStderr string) []sourceEditContextTarget {
	byPath := map[string]int{}
	var targets []sourceEditContextTarget
	add := func(path string, line int, rejectFile bool) {
		path = strings.TrimSpace(strings.TrimSuffix(path, ".rej"))
		if path == "" {
			return
		}
		if existing, ok := byPath[path]; ok {
			if line > 0 && existing >= 0 {
				targets[existing].Line = line
				targets[existing].Targeted = true
			}
			if rejectFile {
				targets[existing].RejectFile = true
			}
			return
		}
		byPath[path] = len(targets)
		targets = append(targets, sourceEditContextTarget{
			Path:       path,
			Line:       line,
			Targeted:   line > 0,
			RejectFile: rejectFile,
		})
	}
	if strings.TrimSpace(editFile) != "" {
		add(editFile, 0, false)
		targets[0].Primary = true
	}
	for _, match := range patchFailedPathLinePattern.FindAllStringSubmatch(patchStderr, -1) {
		if len(match) != 3 {
			continue
		}
		line, err := strconv.Atoi(match[2])
		if err != nil || line <= 0 {
			line = 0
		}
		add(match[1], line, false)
	}
	for _, match := range rejectedPatchFilePattern.FindAllStringSubmatch(patchStderr, -1) {
		if len(match) == 2 {
			add(match[1], 0, true)
		}
	}
	if len(targets) == 0 {
		return []sourceEditContextTarget{{Path: editFile, Primary: true}}
	}
	if len(targets) > 8 {
		targets = targets[:8]
	}
	return targets
}

func writeSourceEditFileContext(ctx context.Context, b *strings.Builder, root string, target sourceEditContextTarget) {
	b.WriteString("file:")
	b.WriteString(target.Path)
	if target.Primary {
		b.WriteString(" primary:true")
	}
	if target.RejectFile {
		b.WriteString(" rejected:true")
	}
	b.WriteString("\n\nfile_context:\n")
	path, err := repoFilePath(root, target.Path)
	if err != nil {
		b.WriteString(err.Error())
		b.WriteByte('\n')
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.WriteString(err.Error())
		b.WriteByte('\n')
	} else {
		lines := contentLines(string(data))
		const maxLines = 160
		start, end, targetLine, targeted := sourceEditContextWindow(lines, target.Line, maxLines)
		if targeted {
			fmt.Fprintf(b, "[showing lines %d-%d of %d around failed patch line %d]\n", start+1, end, len(lines), targetLine)
			if start > 0 {
				fmt.Fprintf(b, "[omitted lines 1-%d]\n", start)
			}
		}
		for i := start; i < end; i++ {
			line := lines[i]
			if len(line) > 300 {
				line = line[:300] + " [truncated]"
			}
			fmt.Fprintf(b, "%4d | %s\n", i+1, line)
		}
		if end < len(lines) {
			if targeted {
				fmt.Fprintf(b, "[omitted lines %d-%d]\n", end+1, len(lines))
			} else {
				fmt.Fprintf(b, "[truncated after %d lines]\n", maxLines)
			}
		}
	}
	diff, err := gitrepo.Run(ctx, root, "diff", "--", target.Path)
	if err == nil && strings.TrimSpace(diff) != "" {
		b.WriteString("\ncurrent_diff:\n")
		if len(diff) > 12<<10 {
			diff = diff[:12<<10] + "\n[diff truncated]\n"
		}
		b.WriteString(diff)
		if !strings.HasSuffix(diff, "\n") {
			b.WriteByte('\n')
		}
	}
}

var patchFailedLinePattern = regexp.MustCompile(`(?m)patch failed: .*:(\d+)$`)
var patchFailedPathLinePattern = regexp.MustCompile(`(?m)patch failed: ([^:\n]+):(\d+)$`)
var rejectedPatchFilePattern = regexp.MustCompile(`(?m)^file:([^\n]+)$`)

func sourceEditContextWindow(lines []string, targetLine, maxLines int) (start, end int, line int, targeted bool) {
	if maxLines <= 0 || len(lines) <= maxLines {
		return 0, len(lines), 0, false
	}
	if targetLine <= 0 {
		return 0, min(len(lines), maxLines), 0, false
	}
	targetIndex := max(0, min(len(lines)-1, targetLine-1))
	half := maxLines / 2
	start = max(0, targetIndex-half)
	end = min(len(lines), start+maxLines)
	if end-start < maxLines {
		start = max(0, end-maxLines)
	}
	return start, end, targetLine, true
}

func failedPatchLine(stderr string) (int, bool) {
	match := patchFailedLinePattern.FindStringSubmatch(stderr)
	if len(match) != 2 {
		return 0, false
	}
	line, err := strconv.Atoi(match[1])
	if err != nil || line <= 0 {
		return 0, false
	}
	return line, true
}

func repoFilePath(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("repo path %q is absolute", rel)
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == "." || clean != rel {
		return "", fmt.Errorf("repo path %q is not clean", rel)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return "", fmt.Errorf("repo path %q escapes repo root", rel)
		}
	}
	return filepath.Join(root, filepath.FromSlash(clean)), nil
}

func artifactRef(path string) string {
	if path == "" {
		return ""
	}
	return "artifact:" + path
}

func artifactPreview(store artifact.Store, path string, limit int) string {
	if path == "" || limit <= 0 {
		return ""
	}
	data, err := store.Read(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	text := string(data)
	if len(text) <= limit {
		return text
	}
	head := limit * 2 / 3
	tail := limit - head
	return text[:head] + fmt.Sprintf("\n[preview truncated; %d bytes omitted]\n", len(text)-limit) + text[len(text)-tail:]
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sanitizePatchPayload(patch []byte) []byte {
	lines := strings.SplitAfter(string(patch), "\n")
	start := 0
	end := len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	removedOpeningFence := false
	if start < end && patchFenceLine(lines[start]) {
		start++
		removedOpeningFence = true
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if removedOpeningFence && end > start && patchFenceLine(lines[end-1]) {
		end--
	}
	var b strings.Builder
	for _, line := range lines[:start] {
		if strings.TrimSpace(line) == "" {
			b.WriteString(line)
		}
	}
	for _, line := range lines[start:end] {
		b.WriteString(line)
	}
	for _, line := range lines[end:] {
		if strings.TrimSpace(line) == "" {
			b.WriteString(line)
		}
	}
	return []byte(b.String())
}

func patchFenceLine(line string) bool {
	switch strings.TrimSpace(line) {
	case "```", "```diff", "```patch":
		return true
	default:
		return false
	}
}

func editContentPath(edit stream.EditIntent) (string, error) {
	content := strings.TrimSpace(edit.Content)
	if content == "" {
		content = "artifact:" + edit.File
	}
	path, ok := strings.CutPrefix(content, "artifact:")
	if !ok || path == "" {
		return "", fmt.Errorf("patch edit content %q is not an artifact ref", edit.Content)
	}
	if err := artifact.ValidatePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func artifactByPath(records []artifact.Record, path string) (artifact.Record, bool) {
	for _, record := range records {
		if record.Path == path {
			return record, true
		}
	}
	return artifact.Record{}, false
}
