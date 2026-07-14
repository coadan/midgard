package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type CommandPolicy struct {
	AllowedRoots []string
	EnvAllowlist []string
	Limits       OutputLimits
	ReadOnly     bool
}

type CommandDeniedError struct {
	Reason string
}

func (e CommandDeniedError) Error() string {
	return "command denied by policy: " + e.Reason
}

func DefaultCommandPolicy(allowedRoots ...string) CommandPolicy {
	return CommandPolicy{
		AllowedRoots: allowedRoots,
		EnvAllowlist: []string{"PATH", "HOME", "TMPDIR", "TEMP", "TMP"},
		Limits:       DefaultOutputLimits(),
	}
}

func ReadOnlyCommandPolicy(allowedRoots ...string) CommandPolicy {
	commandPolicy := DefaultCommandPolicy(allowedRoots...)
	commandPolicy.ReadOnly = true
	return commandPolicy
}

func (p CommandPolicy) ValidateCommand(command string) error {
	if !p.ReadOnly {
		return nil
	}
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return CommandDeniedError{Reason: "command is empty"}
	}
	if hasUnsafeShellSyntax(trimmed) {
		return CommandDeniedError{Reason: "reviewer commands cannot use shell control, redirection, or expansion"}
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return CommandDeniedError{Reason: "command is empty"}
	}
	for _, field := range fields[1:] {
		clean := strings.Trim(field, "'\"")
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || strings.HasPrefix(clean, "~") {
			return CommandDeniedError{Reason: "reviewer command paths must stay inside the review worktree"}
		}
	}
	name := filepath.Base(fields[0])
	args := fields[1:]
	switch name {
	case "rg", "grep", "cat", "head", "tail", "wc", "ls", "stat":
		return nil
	case "sed":
		if slices.Contains(args, "-n") {
			return nil
		}
		return CommandDeniedError{Reason: "reviewer sed commands must use -n"}
	case "find":
		for _, arg := range args {
			switch arg {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir":
				return CommandDeniedError{Reason: "mutating find actions are not allowed for reviewers"}
			}
		}
		return nil
	case "git":
		return validateReadOnlyGit(args)
	case "go":
		return validateReadOnlySubcommand(name, args, "test", "vet", "list")
	case "pnpm", "npm":
		return validateReadOnlySubcommand(name, args, "test")
	case "cargo":
		return validateReadOnlySubcommand(name, args, "test", "check")
	case "pytest":
		return nil
	case "python", "python3":
		if len(args) >= 2 && args[0] == "-m" && args[1] == "pytest" {
			return nil
		}
		return CommandDeniedError{Reason: "reviewer Python commands are limited to python -m pytest"}
	default:
		return CommandDeniedError{Reason: fmt.Sprintf("%q is not in the reviewer command allowlist", name)}
	}
}

func hasUnsafeShellSyntax(command string) bool {
	var quote rune
	escaped := false
	for _, char := range command {
		if escaped {
			escaped = false
			continue
		}
		switch quote {
		case '\'':
			if char == '\'' {
				quote = 0
			}
			continue
		case '"':
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				quote = 0
				continue
			}
			if char == '$' || char == '`' {
				return true
			}
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
		case '\n', '\r', ';', '&', '|', '>', '<', '`', '$':
			return true
		case '\\':
			escaped = true
		}
	}
	return quote != 0
}

func validateReadOnlyGit(args []string) error {
	if len(args) == 0 {
		return CommandDeniedError{Reason: "git subcommand is required"}
	}
	allowed := []string{"diff", "status", "show", "log", "grep", "ls-files", "rev-parse", "branch", "tag"}
	if slices.Contains(allowed, args[0]) {
		return nil
	}
	return CommandDeniedError{Reason: fmt.Sprintf("git %s is not allowed for reviewers", args[0])}
}

func validateReadOnlySubcommand(name string, args []string, allowed ...string) error {
	if len(args) > 0 && slices.Contains(allowed, args[0]) {
		return nil
	}
	return CommandDeniedError{Reason: fmt.Sprintf("%s command is not allowed for reviewers", name)}
}

func (p CommandPolicy) ValidateCWD(cwd string) error {
	if cwd == "" {
		return fmt.Errorf("cwd is required")
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}
	for _, root := range p.AllowedRoots {
		inside, err := isInside(root, absCWD)
		if err != nil {
			return err
		}
		if inside {
			return nil
		}
	}
	return fmt.Errorf("cwd %q is outside allowed roots", cwd)
}

func (p CommandPolicy) Environment(extra map[string]string) []string {
	allowed := map[string]bool{}
	for _, key := range p.EnvAllowlist {
		allowed[key] = true
	}
	env := make([]string, 0, len(allowed)+len(extra))
	for _, pair := range os.Environ() {
		key, _, ok := strings.Cut(pair, "=")
		if ok && allowed[key] {
			env = append(env, pair)
		}
	}
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if allowed[key] {
			env = append(env, key+"="+extra[key])
		}
	}
	return env
}

func isInside(root, path string) (bool, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(absRoot, path)
	if err != nil {
		return false, err
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."), nil
}
