package gitrepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type ApplyPatchError struct {
	Err     error
	Stderr  string
	Partial bool
}

// UniqueReplacementResult binds a deterministic fallback edit to its exact content.
type UniqueReplacementResult struct {
	File            string
	BeforeChecksum  string
	AfterChecksum   string
	RemovedChecksum string
	AddedChecksum   string
	RemovedBytes    int
	AddedBytes      int
}

// UniqueReplacementError explains why a failed patch was not eligible for fallback.
type UniqueReplacementError struct {
	Reason string
}

func (e *UniqueReplacementError) Error() string {
	return "unique replacement is not safe: " + e.Reason
}

func (e *ApplyPatchError) Error() string {
	details := strings.TrimSpace(e.Stderr)
	prefix := "git apply"
	if e.Partial {
		prefix = "git apply partially applied patch with rejects"
	}
	if details == "" {
		return fmt.Sprintf("%s: %v", prefix, e.Err)
	}
	return fmt.Sprintf("%s: %v: %s", prefix, e.Err, details)
}

func (e *ApplyPatchError) Unwrap() error {
	return e.Err
}

func Diff(ctx context.Context, path string) (string, error) {
	return Run(ctx, path, "diff", "--")
}

func ApplyPatch(ctx context.Context, path string, patch []byte) error {
	out, err := runApplyPatch(ctx, path, patch, "apply", "--whitespace=nowarn", "--recount", "-")
	if err != nil {
		return &ApplyPatchError{Err: err, Stderr: strings.TrimSpace(out.Stderr)}
	}
	return nil
}

func ApplyPatchWithRejects(ctx context.Context, path string, patch []byte) error {
	before, _ := WorktreeStatus(ctx, path)
	if err := ApplyPatch(ctx, path, patch); err == nil {
		return nil
	} else {
		if whitespaceErr := applyPatchIgnoringContextWhitespace(ctx, path, patch); whitespaceErr == nil {
			return nil
		}
		beforeRejects := rejectFileSet(path)
		out, rejectErr := runApplyPatch(ctx, path, patch, "apply", "--reject", "--whitespace=nowarn", "--recount", "-")
		rejects := readAndRemoveNewRejectFiles(path, beforeRejects)
		if rejectErr == nil {
			return nil
		}
		after, _ := WorktreeStatus(ctx, path)
		return &ApplyPatchError{
			Err:     rejectErr,
			Stderr:  combineRejectApplyDiagnostics(err, out, rejects),
			Partial: before.Porcelain != after.Porcelain,
		}
	}
}

// ApplyUniqueReplacement applies one exact, line-bounded replacement from a
// single-file, single-hunk patch when the removed content has one unique match.
func ApplyUniqueReplacement(root, declaredFile string, patch []byte) (UniqueReplacementResult, error) {
	removed, added, err := parseUniqueReplacementPatch(declaredFile, patch)
	if err != nil {
		return UniqueReplacementResult{}, err
	}
	target, err := resolveWorktreeFile(root, declaredFile)
	if err != nil {
		return UniqueReplacementResult{}, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return UniqueReplacementResult{}, &UniqueReplacementError{Reason: fmt.Sprintf("read target metadata: %v", err)}
	}
	if !info.Mode().IsRegular() {
		return UniqueReplacementResult{}, &UniqueReplacementError{Reason: "target is not a regular file"}
	}
	before, err := os.ReadFile(target)
	if err != nil {
		return UniqueReplacementResult{}, &UniqueReplacementError{Reason: fmt.Sprintf("read target: %v", err)}
	}
	index, matches := uniqueLineBoundedMatch(before, removed)
	if matches != 1 {
		return UniqueReplacementResult{}, &UniqueReplacementError{Reason: fmt.Sprintf("removed sequence has %d line-bounded matches, want exactly 1", matches)}
	}
	after := make([]byte, 0, len(before)-len(removed)+len(added))
	after = append(after, before[:index]...)
	after = append(after, added...)
	after = append(after, before[index+len(removed):]...)
	if bytes.Equal(before, after) {
		return UniqueReplacementResult{}, &UniqueReplacementError{Reason: "replacement would not change the target"}
	}
	if err := writeFileAtomically(target, after, info.Mode().Perm()); err != nil {
		return UniqueReplacementResult{}, &UniqueReplacementError{Reason: fmt.Sprintf("write target: %v", err)}
	}
	return UniqueReplacementResult{
		File:            declaredFile,
		BeforeChecksum:  contentChecksum(before),
		AfterChecksum:   contentChecksum(after),
		RemovedChecksum: contentChecksum(removed),
		AddedChecksum:   contentChecksum(added),
		RemovedBytes:    len(removed),
		AddedBytes:      len(added),
	}, nil
}

func parseUniqueReplacementPatch(declaredFile string, patch []byte) ([]byte, []byte, error) {
	clean, err := cleanWorktreeRelativePath(declaredFile)
	if err != nil {
		return nil, nil, err
	}
	if bytes.IndexByte(patch, 0) >= 0 {
		return nil, nil, &UniqueReplacementError{Reason: "patch contains NUL bytes"}
	}
	var oldPath, newPath string
	var removed, added bytes.Buffer
	var hunkSeen, inHunk, inChange bool
	changeBlocks := 0
	lines := strings.SplitAfter(string(patch), "\n")
	for index, raw := range lines {
		if raw == "" && index == len(lines)-1 {
			continue
		}
		line := strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
		if strings.Contains(line, "GIT binary patch") || strings.HasPrefix(line, "Binary files ") {
			return nil, nil, &UniqueReplacementError{Reason: "binary patches are not eligible"}
		}
		if !inHunk {
			switch {
			case strings.TrimSpace(line) == "":
				continue
			case strings.HasPrefix(line, "diff --git "):
				if oldPath != "" || newPath != "" || hunkSeen {
					return nil, nil, &UniqueReplacementError{Reason: "patch contains multiple file sections"}
				}
				fields := strings.Fields(line)
				if len(fields) != 4 || fields[2] != "a/"+clean || fields[3] != "b/"+clean {
					return nil, nil, &UniqueReplacementError{Reason: "diff header does not match the declared file"}
				}
			case strings.HasPrefix(line, "index "):
				continue
			case strings.HasPrefix(line, "--- "):
				if oldPath != "" {
					return nil, nil, &UniqueReplacementError{Reason: "patch contains multiple old-file headers"}
				}
				oldPath = patchHeaderPath(strings.TrimPrefix(line, "--- "))
			case strings.HasPrefix(line, "+++ "):
				if newPath != "" {
					return nil, nil, &UniqueReplacementError{Reason: "patch contains multiple new-file headers"}
				}
				newPath = patchHeaderPath(strings.TrimPrefix(line, "+++ "))
			case strings.HasPrefix(line, "@@ "):
				if oldPath != "a/"+clean || newPath != "b/"+clean {
					return nil, nil, &UniqueReplacementError{Reason: "patch paths do not match the declared file"}
				}
				if hunkSeen {
					return nil, nil, &UniqueReplacementError{Reason: "patch contains multiple hunks"}
				}
				hunkSeen = true
				inHunk = true
			default:
				return nil, nil, &UniqueReplacementError{Reason: fmt.Sprintf("unsupported patch metadata %q", line)}
			}
			continue
		}

		if strings.HasPrefix(line, "@@ ") {
			return nil, nil, &UniqueReplacementError{Reason: "patch contains multiple hunks"}
		}
		if line == `\ No newline at end of file` {
			return nil, nil, &UniqueReplacementError{Reason: "no-newline markers are not eligible"}
		}
		if len(raw) == 0 {
			return nil, nil, &UniqueReplacementError{Reason: "malformed empty hunk line"}
		}
		switch raw[0] {
		case ' ':
			inChange = false
		case '-':
			if !inChange {
				changeBlocks++
				inChange = true
			}
			removed.WriteString(raw[1:])
		case '+':
			if !inChange {
				changeBlocks++
				inChange = true
			}
			added.WriteString(raw[1:])
		default:
			return nil, nil, &UniqueReplacementError{Reason: fmt.Sprintf("malformed hunk line %q", line)}
		}
	}
	if !hunkSeen {
		return nil, nil, &UniqueReplacementError{Reason: "patch has no hunk"}
	}
	if changeBlocks != 1 {
		return nil, nil, &UniqueReplacementError{Reason: fmt.Sprintf("patch has %d change blocks, want exactly 1", changeBlocks)}
	}
	if removed.Len() == 0 || added.Len() == 0 {
		return nil, nil, &UniqueReplacementError{Reason: "replacement requires both removed and added content"}
	}
	if bytes.Equal(removed.Bytes(), added.Bytes()) {
		return nil, nil, &UniqueReplacementError{Reason: "removed and added content are identical"}
	}
	return removed.Bytes(), added.Bytes(), nil
}

func patchHeaderPath(value string) string {
	path, _, _ := strings.Cut(value, "\t")
	return strings.TrimSpace(path)
}

func cleanWorktreeRelativePath(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) {
		return "", &UniqueReplacementError{Reason: "declared file path is empty or absolute"}
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean != path || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", &UniqueReplacementError{Reason: "declared file path is not clean"}
	}
	return clean, nil
}

func resolveWorktreeFile(root, path string) (string, error) {
	clean, err := cleanWorktreeRelativePath(path)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(clean)), nil
}

func uniqueLineBoundedMatch(data, target []byte) (int, int) {
	if len(target) == 0 {
		return -1, 0
	}
	first := -1
	matches := 0
	for offset := 0; offset <= len(data)-len(target); {
		relative := bytes.Index(data[offset:], target)
		if relative < 0 {
			break
		}
		index := offset + relative
		end := index + len(target)
		startBounded := index == 0 || data[index-1] == '\n'
		endBounded := end == len(data) || target[len(target)-1] == '\n' || data[end] == '\n'
		if startBounded && endBounded {
			if first < 0 {
				first = index
			}
			matches++
		}
		offset = index + 1
	}
	return first, matches
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".midgard-edit-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func contentChecksum(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func applyPatchIgnoringContextWhitespace(ctx context.Context, path string, patch []byte) error {
	out, err := runApplyPatch(ctx, path, patch, "apply", "--ignore-space-change", "--whitespace=nowarn", "--recount", "-")
	if err != nil {
		return &ApplyPatchError{Err: err, Stderr: strings.TrimSpace(out.Stderr)}
	}
	return nil
}

type applyPatchOutput struct {
	Stdout string
	Stderr string
}

func runApplyPatch(ctx context.Context, path string, patch []byte, args ...string) (applyPatchOutput, error) {
	cmd := exec.CommandContext(ctx, "git", "apply", "--whitespace=nowarn", "--recount", "-")
	if len(args) > 0 {
		cmd = exec.CommandContext(ctx, "git", args...)
	}
	cmd.Dir = path
	cmd.Stdin = bytes.NewReader(patch)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return applyPatchOutput{Stdout: strings.TrimSpace(stdout.String()), Stderr: strings.TrimSpace(stderr.String())}, err
	}
	return applyPatchOutput{Stdout: strings.TrimSpace(stdout.String()), Stderr: strings.TrimSpace(stderr.String())}, nil
}

func combineRejectApplyDiagnostics(directErr error, out applyPatchOutput, rejects string) string {
	var parts []string
	if directErr != nil {
		parts = append(parts, "direct git apply:\n"+strings.TrimSpace(directErr.Error()))
	}
	if diagnostics := strings.TrimSpace(strings.Join([]string{out.Stdout, out.Stderr}, "\n")); diagnostics != "" {
		parts = append(parts, "git apply --reject:\n"+diagnostics)
	}
	if strings.TrimSpace(rejects) != "" {
		parts = append(parts, "rejected hunks:\n"+strings.TrimSpace(rejects))
	}
	return strings.Join(parts, "\n\n")
}

func rejectFileSet(root string) map[string]bool {
	files := map[string]bool{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".rej") {
			return nil
		}
		if rel, err := filepath.Rel(root, path); err == nil {
			files[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	return files
}

func readAndRemoveNewRejectFiles(root string, before map[string]bool) string {
	type rejectFile struct {
		path string
		body string
	}
	var files []rejectFile
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".rej") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if before[rel] {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			data = []byte(readErr.Error())
		}
		files = append(files, rejectFile{path: rel, body: previewReject(data, 8192)})
		_ = os.Remove(path)
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	var b strings.Builder
	for i, file := range files {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("file:")
		b.WriteString(file.path)
		b.WriteByte('\n')
		b.WriteString(file.body)
	}
	return b.String()
}

func previewReject(data []byte, limit int) string {
	if len(data) <= limit {
		return string(data)
	}
	head := limit * 2 / 3
	tail := limit - head
	return string(data[:head]) + fmt.Sprintf("\n[reject truncated; %d bytes omitted]\n", len(data)-limit) + string(data[len(data)-tail:])
}
