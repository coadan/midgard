package gitrepo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func AddWorktree(ctx context.Context, repoPath, worktreePath, branch, startRef string) error {
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return err
	}
	_, err := Run(ctx, repoPath, "worktree", "add", "-b", branch, worktreePath, startRef)
	return err
}

func AddSnapshotWorktree(ctx context.Context, sourcePath, snapshotPath string) (err error) {
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o755); err != nil {
		return err
	}
	if _, err := Run(ctx, sourcePath, "worktree", "add", "--detach", snapshotPath, "HEAD"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = RemoveSnapshotWorktree(context.WithoutCancel(ctx), sourcePath, snapshotPath)
		}
	}()
	diff, err := Run(ctx, sourcePath, "diff", "--binary", "HEAD", "--")
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) != "" {
		if err := ApplyPatch(ctx, snapshotPath, []byte(diff+"\n")); err != nil {
			return err
		}
	}
	untracked, err := Run(ctx, sourcePath, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, rel := range strings.Split(untracked, "\x00") {
		if rel == "" {
			continue
		}
		if err := copySnapshotFile(sourcePath, snapshotPath, rel); err != nil {
			return err
		}
	}
	return nil
}

func RemoveSnapshotWorktree(ctx context.Context, sourcePath, snapshotPath string) error {
	_, err := Run(ctx, sourcePath, "worktree", "remove", "--force", snapshotPath)
	removeErr := os.RemoveAll(snapshotPath)
	if err != nil {
		return err
	}
	return removeErr
}

func copySnapshotFile(sourceRoot, snapshotRoot, rel string) error {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("untracked snapshot path %q escapes worktree", rel)
	}
	source := filepath.Join(sourceRoot, clean)
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	target := filepath.Join(snapshotRoot, clean)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, info.Mode().Perm())
}
