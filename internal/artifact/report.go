package artifact

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

var roleReportPaths = map[string]string{
	"planner":     "plan.mdx",
	"implementer": "implementation.mdx",
	"reviewer":    "review.mdx",
	"compactor":   "compaction.mdx",
}

func ValidateReportPath(role, path string) error {
	expected, err := RoleReportPath(role)
	if err != nil {
		return err
	}
	if path != expected {
		return fmt.Errorf("report path %q is not allowed for role %q", path, role)
	}
	return ValidatePath(path)
}

func RoleReportPath(role string) (string, error) {
	path, ok := roleReportPaths[role]
	if !ok {
		return "", fmt.Errorf("unknown role %q", role)
	}
	return path, nil
}

func ValidateSafeMDX(data []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "import ") || strings.HasPrefix(lower, "export ") {
			return fmt.Errorf("safe MDX disallows import/export")
		}
		if strings.Contains(lower, "<script") {
			return fmt.Errorf("safe MDX disallows script tags")
		}
	}
	return scanner.Err()
}
