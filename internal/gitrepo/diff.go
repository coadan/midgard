package gitrepo

import (
	"bytes"
	"context"
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
		status, _ := WorktreeStatus(ctx, path)
		return &ApplyPatchError{
			Err:     rejectErr,
			Stderr:  combineRejectApplyDiagnostics(err, out, rejects),
			Partial: status.Dirty,
		}
	}
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
