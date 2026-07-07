package artifact

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

var allowedRoleReports = map[string]map[string]bool{
	"planner":     {"plan.mdx": true},
	"implementer": {"implementation.mdx": true},
	"reviewer":    {"review.mdx": true},
	"compactor":   {"compaction.mdx": true},
}

func ValidateReportPath(role, path string) error {
	allowed, ok := allowedRoleReports[role]
	if !ok {
		return fmt.Errorf("unknown role %q", role)
	}
	if !allowed[path] {
		return fmt.Errorf("report path %q is not allowed for role %q", path, role)
	}
	return ValidatePath(path)
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
