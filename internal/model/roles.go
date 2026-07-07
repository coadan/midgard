package model

import "fmt"

type Role string

const (
	RolePlanner     Role = "planner"
	RoleImplementer Role = "implementer"
	RoleReviewer    Role = "reviewer"
	RoleCompactor   Role = "compactor"
)

func (r Role) ReportPath() (string, error) {
	switch r {
	case RolePlanner:
		return "plan.mdx", nil
	case RoleImplementer:
		return "implementation.mdx", nil
	case RoleReviewer:
		return "review.mdx", nil
	case RoleCompactor:
		return "compaction.mdx", nil
	default:
		return "", fmt.Errorf("unknown role %q", r)
	}
}

func (r Role) String() string {
	return string(r)
}
