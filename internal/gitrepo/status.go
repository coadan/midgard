package gitrepo

import "context"

type Status struct {
	Porcelain string
	Dirty     bool
}

func WorktreeStatus(ctx context.Context, path string) (Status, error) {
	out, err := Run(ctx, path, "status", "--porcelain")
	if err != nil {
		return Status{}, err
	}
	return Status{Porcelain: out, Dirty: out != ""}, nil
}
