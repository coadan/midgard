package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"midgard/internal/action"
	runtimeenv "midgard/internal/environment"
)

const DefaultMaxOutput = 1 << 20

type Output struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	ExitCode    int    `json:"exit_code"`
	Truncated   bool   `json:"truncated"`
	ErrorCode   string `json:"error_code,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	JobID       string `json:"job_id,omitempty"`
	Status      string `json:"status,omitempty"`
	JobExitCode *int   `json:"job_exit_code,omitempty"`
}

// Sandbox is required for general shell actions. Its implementation, not the
// kernel, must enforce filesystem and process isolation.
type Sandbox interface {
	RunShell(context.Context, string, string, map[string]string, int) (Output, error)
	RunArgv(context.Context, string, []string, map[string]string, int) (Output, error)
}

// UnsafeExecutor is an explicit prototype escape hatch. Unlike Sandbox, it
// makes no containment claim and must never be selected implicitly.
type UnsafeExecutor interface {
	RunShell(context.Context, string, string, map[string]string, int) (Output, error)
	RunArgv(context.Context, string, []string, map[string]string, int) (Output, error)
}

type EnvironmentResolver interface {
	Resolve(context.Context, string) (runtimeenv.Resolved, error)
}

type Runner struct {
	Actions       *action.Service
	Binding       Binding
	Sandbox       Sandbox
	Unsafe        UnsafeExecutor
	AllowedChecks [][]string
	MaxOutput     int
	Environment   EnvironmentResolver
	Jobs          *BackgroundJobs
	// YggBinary is the exact bundled repository-search executable. It is
	// intentionally supplied by the runtime rather than discovered on PATH.
	YggBinary          string
	YggStorageRoot     string
	YggConfiguration   []byte
	YggSemantic        bool
	YggEmbeddingAPIKey string
	// HeimdalBinary is the exact bundled browser automation executable.
	HeimdalBinary string
}

func (r Runner) Execute(ctx context.Context, claim action.Claim) (json.RawMessage, error) {
	current, err := r.Actions.Get(ctx, claim.ActionID)
	if err != nil {
		return nil, err
	}
	if current.State != action.StateDispatched || current.CommitID != claim.CommitID || current.DispatchOwner != claim.Owner || current.DispatchFence != claim.Fence {
		return nil, errors.New("stale or undispatched action claim")
	}
	if current.SessionID != r.Binding.SessionID {
		return nil, errors.New("action is not bound to this workspace")
	}
	var sessionStatus string
	if err := r.Actions.Log.DB().QueryRowContext(ctx, `SELECT status FROM session_projection WHERE session_id=?`, current.SessionID).Scan(&sessionStatus); err != nil {
		return nil, err
	}
	if sessionStatus != "active" {
		return nil, fmt.Errorf("session %s is %s", current.SessionID, sessionStatus)
	}
	limit := r.MaxOutput
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	var output Output
	var commandEnvironment map[string]string
	var secretValues map[string]string
	if current.Capability == "check.run" || current.Capability == "shell" {
		var metadata struct {
			EnvironmentRevision string `json:"_midgard_environment"`
		}
		if err := json.Unmarshal(current.Arguments, &metadata); err != nil {
			return nil, err
		}
		if metadata.EnvironmentRevision != "" {
			if r.Environment == nil {
				return nil, errors.New("committed runtime environment is unavailable")
			}
			resolved, err := r.Environment.Resolve(ctx, metadata.EnvironmentRevision)
			if err != nil {
				return nil, err
			}
			commandEnvironment, secretValues = resolved.Values, resolved.Secrets
		}
	}
	switch current.Capability {
	case "repo.search":
		var args struct {
			Query string `json:"query"`
			Path  string `json:"path"`
		}
		if err := json.Unmarshal(current.Arguments, &args); err != nil {
			return nil, err
		}
		output, err = searchRepository(ctx, r.YggBinary, r.YggStorageRoot, r.YggConfiguration, r.YggEmbeddingAPIKey, r.YggSemantic, r.Binding.WorktreeRoot, args.Query, args.Path, limit)
	case "browser.run":
		var args struct {
			Command        string `json:"command"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		if err := json.Unmarshal(current.Arguments, &args); err != nil {
			return nil, err
		}
		arguments, parseErr := ParseCommand(args.Command)
		if parseErr != nil {
			return nil, parseErr
		}
		output, err = runHeimdal(ctx, r.HeimdalBinary, r.Binding.WorktreeRoot, arguments, args.TimeoutSeconds, limit)
	case "file.inspect":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(current.Arguments, &args); err != nil {
			return nil, err
		}
		data, err := readBounded(r.Binding.WorktreeRoot, args.Path, int64(limit))
		if err != nil {
			return nil, err
		}
		output.Stdout = string(data)
		digest := sha256.Sum256(data)
		output.SHA256 = "sha256:" + hex.EncodeToString(digest[:])
	case "git.diff":
		output, err = runArgv(ctx, r.Binding.WorktreeRoot, limit, "git", "diff", "--no-ext-diff", "--")
	case "patch.apply":
		var args struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal(current.Arguments, &args); err != nil {
			return nil, err
		}
		output, err = applyPatch(ctx, r.Binding.WorktreeRoot, []byte(args.Patch), limit)
	case "file.replace":
		var args struct {
			Path           string `json:"path"`
			ExpectedSHA256 string `json:"expected_sha256"`
			Content        string `json:"content"`
		}
		if err := json.Unmarshal(current.Arguments, &args); err != nil {
			return nil, err
		}
		output, err = replaceFile(r.Binding.WorktreeRoot, args.Path, args.ExpectedSHA256, []byte(args.Content))
	case "check.run":
		var args struct {
			Argv []string `json:"argv"`
		}
		if err := json.Unmarshal(current.Arguments, &args); err != nil || len(args.Argv) == 0 {
			return nil, errors.New("check.run requires non-empty argv")
		}
		if r.Unsafe == nil && !allowedArgv(args.Argv, r.AllowedChecks) {
			return nil, errors.New("check.run argv is not authorized by policy")
		}
		if r.Sandbox == nil && r.Unsafe == nil {
			return nil, errors.New("check.run requires a configured containment sandbox")
		}
		if r.Sandbox != nil {
			output, err = r.Sandbox.RunArgv(ctx, r.Binding.WorktreeRoot, args.Argv, commandEnvironment, limit)
		} else {
			output, err = r.Unsafe.RunArgv(ctx, r.Binding.WorktreeRoot, args.Argv, commandEnvironment, limit)
		}
	case "shell":
		if r.Sandbox == nil && r.Unsafe == nil {
			return nil, errors.New("shell action requires a configured containment sandbox")
		}
		var args struct {
			Command        string `json:"command"`
			TimeoutSeconds int    `json:"timeout_seconds"`
			Background     bool   `json:"background"`
		}
		if err := json.Unmarshal(current.Arguments, &args); err != nil || args.Command == "" {
			return nil, errors.New("shell requires command")
		}
		if args.Background {
			if r.Unsafe == nil || r.Jobs == nil {
				return nil, errors.New("background shell work is unavailable in this runtime")
			}
			output, err = r.Jobs.Start(ctx, current.SessionID, r.Binding.RepositoryName, r.Binding.WorktreeRoot, args.Command, commandEnvironment, secretValues, limit)
			break
		}
		timeout := 60 * time.Second
		if args.TimeoutSeconds > 0 {
			timeout = time.Duration(args.TimeoutSeconds) * time.Second
		}
		shellContext, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if r.Sandbox != nil {
			output, err = r.Sandbox.RunShell(shellContext, r.Binding.WorktreeRoot, args.Command, commandEnvironment, limit)
		} else {
			output, err = r.Unsafe.RunShell(shellContext, r.Binding.WorktreeRoot, args.Command, commandEnvironment, limit)
		}
	case "shell.poll":
		if r.Jobs == nil {
			return nil, errors.New("background shell work is unavailable in this runtime")
		}
		var args struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(current.Arguments, &args); err != nil {
			return nil, err
		}
		output, err = r.Jobs.Poll(current.SessionID, args.JobID)
	case "shell.stop":
		if r.Jobs == nil {
			return nil, errors.New("background shell work is unavailable in this runtime")
		}
		var args struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(current.Arguments, &args); err != nil {
			return nil, err
		}
		output, err = r.Jobs.Stop(ctx, current.SessionID, args.JobID)
	default:
		return nil, fmt.Errorf("unsupported workspace capability %q", current.Capability)
	}
	if err != nil && output.ExitCode == 0 {
		return nil, redactError(err, secretValues)
	}
	output.Stdout = redact(output.Stdout, secretValues)
	output.Stderr = redact(output.Stderr, secretValues)
	return json.Marshal(output)
}

func redact(value string, secrets map[string]string) string {
	for key, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED:"+key+"]")
		}
	}
	return value
}

func redactError(err error, secrets map[string]string) error {
	if err == nil {
		return nil
	}
	return errors.New(redact(err.Error(), secrets))
}

func allowedArgv(got []string, allowed [][]string) bool {
	for _, candidate := range allowed {
		if len(candidate) != len(got) {
			continue
		}
		match := true
		for i := range got {
			if got[i] != candidate[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func readBounded(root, relative string, limit int64) ([]byte, error) {
	path, err := resolveExisting(root, relative)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file output exceeds %d bytes", limit)
	}
	return data, nil
}

func searchRepository(ctx context.Context, yggBinary, storageRoot string, configuration []byte, embeddingAPIKey string, semantic bool, root, query, relative string, limit int) (Output, error) {
	const (
		maxSearchOutput = 64 << 10
		resultLimit     = 8
	)
	if limit <= 0 || limit > maxSearchOutput {
		limit = maxSearchOutput
	}
	if strings.TrimSpace(yggBinary) == "" {
		return unavailableSearchOutput(), errors.New("bundled repository search executable is not configured")
	}
	if info, err := os.Stat(yggBinary); err != nil || info.IsDir() {
		if err == nil {
			err = errors.New("bundled repository search executable is a directory")
		}
		return unavailableSearchOutput(), fmt.Errorf("bundled repository search executable is unavailable: %w", err)
	}
	scope := root
	if relative != "" {
		var err error
		scope, err = resolveExisting(root, relative)
		if err != nil {
			return Output{ExitCode: -1, ErrorCode: "invalid_path"}, err
		}
	}
	environment, err := yggEnvironment(storageRoot, configuration, embeddingAPIKey)
	if err != nil {
		return Output{Stderr: "Midgard could not prepare local repository search. Reinstall Midgard and try again.", ExitCode: -1, ErrorCode: "search_setup_failed"}, err
	}
	mode := "lexical"
	if semantic {
		mode = "auto"
	}
	searchContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(searchContext, yggBinary,
		"search", "--root", scope, "--mode", mode, "--fixed-strings", "--limit", fmt.Sprintf("%d", resultLimit), "--", query)
	command.Dir = root
	command.Env = environment
	raw, err := collect(command, maxSearchOutput)
	if err != nil {
		if errors.Is(searchContext.Err(), context.DeadlineExceeded) {
			return Output{Stderr: "Repository search took too long. Try a narrower path or query.", ExitCode: -1, ErrorCode: "search_timeout", Truncated: raw.Truncated}, searchContext.Err()
		}
		return Output{Stderr: "Local repository search stopped before returning results. Try again; if it keeps happening, reinstall Midgard.", ExitCode: raw.ExitCode, ErrorCode: "search_failed", Truncated: raw.Truncated}, err
	}
	result, err := parseYggSearch(raw.Stdout, root, mode)
	if err != nil {
		return Output{Stderr: "Bundled repository search returned an unexpected result. Reinstall Midgard and try again.", ExitCode: -1, ErrorCode: "search_invalid_result", Truncated: raw.Truncated}, err
	}
	stdout, truncated := formatYggSearch(result, limit)
	return Output{Stdout: stdout, ExitCode: 0, Truncated: raw.Truncated || truncated}, nil
}

func unavailableSearchOutput() Output {
	return Output{Stderr: "Midgard's bundled repository search is unavailable. Reinstall Midgard to restore it.", ExitCode: -1, ErrorCode: "search_unavailable"}
}

func yggEnvironment(storageRoot string, configuration []byte, embeddingAPIKey string) ([]string, error) {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "YGG_STORAGE_ROOT=") || strings.HasPrefix(entry, "YGG_CONFIG=") {
			continue
		}
		environment = append(environment, entry)
	}
	if storageRoot != "" {
		configurationPath, err := writeYggConfiguration(storageRoot, configuration)
		if err != nil {
			return nil, err
		}
		environment = append(environment,
			"YGG_STORAGE_ROOT="+storageRoot,
			"YGG_CONFIG="+configurationPath,
		)
	}
	if embeddingAPIKey != "" {
		environment = append(environment, "MIDGARD_YGG_EMBEDDING_API_KEY="+embeddingAPIKey)
	}
	return append(environment, "GIT_OPTIONAL_LOCKS=0"), nil
}

func writeYggConfiguration(storageRoot string, configuration []byte) (string, error) {
	if len(configuration) == 0 {
		configuration = []byte(`{"schema":"ygg.config/v1"}`)
	}
	if err := os.MkdirAll(storageRoot, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(storageRoot, "midgard-user-config.json")
	temporary, err := os.CreateTemp(storageRoot, ".ygg-config-*.json")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return "", err
	}
	if _, err := temporary.Write(configuration); err != nil {
		cleanup()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return "", err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return "", err
	}
	return path, nil
}

type yggSearchEnvelope struct {
	Schema string          `json:"schema"`
	OK     bool            `json:"ok"`
	Data   json.RawMessage `json:"data"`
}

type yggSearchResult struct {
	Schema     string            `json:"schema"`
	ActiveMode string            `json:"activeMode"`
	Records    []yggSearchRecord `json:"records"`
	MorePaths  []string          `json:"morePaths"`
}

type yggSearchRecord struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Excerpt   string `json:"excerpt"`
}

func parseYggSearch(raw, root, requestedMode string) (yggSearchResult, error) {
	var envelope yggSearchEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return yggSearchResult{}, fmt.Errorf("decode ygg envelope: %w", err)
	}
	if envelope.Schema != "ygg.cli/v1" || !envelope.OK || len(envelope.Data) == 0 {
		return yggSearchResult{}, errors.New("invalid ygg search envelope")
	}
	var result yggSearchResult
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		return yggSearchResult{}, fmt.Errorf("decode ygg search result: %w", err)
	}
	if result.Schema != "ygg.search.result/v4" || !allowedYggMode(requestedMode, result.ActiveMode) {
		return yggSearchResult{}, errors.New("unexpected ygg search result mode or schema")
	}
	for _, record := range result.Records {
		if !validYggPath(root, record.Path) || record.StartLine < 1 || record.EndLine < record.StartLine || strings.TrimSpace(record.Excerpt) == "" {
			return yggSearchResult{}, errors.New("invalid ygg search citation")
		}
	}
	for _, path := range result.MorePaths {
		if !validYggPath(root, path) {
			return yggSearchResult{}, errors.New("invalid ygg follow-up path")
		}
	}
	return result, nil
}

func allowedYggMode(requested, active string) bool {
	if requested == "lexical" {
		return active == "lexical"
	}
	return requested == "auto" && (active == "lexical" || active == "hybrid")
}

func runHeimdal(ctx context.Context, heimdalBinary, root string, arguments []string, timeoutSeconds, limit int) (Output, error) {
	if strings.TrimSpace(heimdalBinary) == "" {
		return unavailableBrowserOutput(), errors.New("bundled browser automation executable is not configured")
	}
	if info, err := os.Stat(heimdalBinary); err != nil || info.IsDir() {
		if err == nil {
			err = errors.New("bundled browser automation executable is a directory")
		}
		return unavailableBrowserOutput(), fmt.Errorf("bundled browser automation executable is unavailable: %w", err)
	}
	timeout := 60 * time.Second
	if timeoutSeconds > 0 {
		timeout = time.Duration(timeoutSeconds) * time.Second
	}
	browserContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(browserContext, heimdalBinary, arguments...)
	command.Dir = root
	command.Env = commandEnvironment(nil)
	output, err := collectProcessGroup(browserContext, command, normalizedLimit(limit))
	if errors.Is(browserContext.Err(), context.DeadlineExceeded) {
		output.ErrorCode = "browser_timeout"
	}
	if err != nil && output.ErrorCode == "" {
		output.ErrorCode = "browser_failed"
	}
	if err != nil && output.ExitCode == 0 {
		output.ExitCode = -1
	}
	return output, err
}

func unavailableBrowserOutput() Output {
	return Output{Stderr: "Midgard's bundled browser automation is unavailable. Reinstall Midgard to restore it.", ExitCode: -1, ErrorCode: "browser_unavailable"}
}

// ParseCommand accepts a deliberately small shell-like argv notation. It
// supports quoted arguments and backslash escapes but performs no expansion,
// substitution, redirection, or operator handling.
func ParseCommand(command string) ([]string, error) {
	var arguments []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			arguments = append(arguments, current.String())
			current.Reset()
		}
	}
	for _, value := range command {
		if escaped {
			current.WriteRune(value)
			escaped = false
			continue
		}
		if value == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if value == quote {
				quote = 0
			} else {
				current.WriteRune(value)
			}
			continue
		}
		if value == '\'' || value == '"' {
			quote = value
			continue
		}
		if value == ' ' || value == '\t' || value == '\n' {
			flush()
			continue
		}
		current.WriteRune(value)
	}
	if escaped || quote != 0 {
		return nil, errors.New("unterminated quote or escape")
	}
	flush()
	if len(arguments) == 0 {
		return nil, errors.New("command is required")
	}
	return arguments, nil
}

func validYggPath(root, value string) bool {
	if value == "" || filepath.IsAbs(value) {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	return within(root, filepath.Join(root, clean))
}

func formatYggSearch(result yggSearchResult, limit int) (string, bool) {
	var output strings.Builder
	truncated := false
	appendLine := func(line string) bool {
		if output.Len()+len(line)+1 > limit {
			truncated = true
			return false
		}
		if output.Len() > 0 {
			output.WriteByte('\n')
		}
		output.WriteString(line)
		return true
	}
	for _, record := range result.Records {
		location := fmt.Sprintf("%s:%d", filepath.ToSlash(record.Path), record.StartLine)
		if record.EndLine > record.StartLine {
			location += fmt.Sprintf("-%d", record.EndLine)
		}
		excerpt := strings.Join(strings.Fields(record.Excerpt), " ")
		if !appendLine(location + ": " + excerpt) {
			return output.String(), true
		}
	}
	if len(result.MorePaths) > 0 {
		if !appendLine("More paths: " + strings.Join(result.MorePaths, ", ")) {
			return output.String(), true
		}
	}
	return output.String(), truncated
}

func resolveExisting(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("path must be relative")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, filepath.Clean(relative)))
	if err != nil {
		return "", err
	}
	if !within(root, path) {
		return "", errors.New("path escapes worktree")
	}
	return path, nil
}

func within(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func applyPatch(ctx context.Context, root string, patch []byte, limit int) (Output, error) {
	if len(patch) > limit {
		return Output{}, fmt.Errorf("patch exceeds %d bytes", limit)
	}
	check := exec.CommandContext(ctx, "git", "apply", "--check", "--whitespace=error", "-")
	check.Dir, check.Stdin = root, bytes.NewReader(patch)
	var checkErr bytes.Buffer
	check.Stderr = &checkErr
	if err := check.Run(); err != nil {
		return Output{Stderr: bounded(checkErr.Bytes(), limit), ExitCode: exitCode(err), ErrorCode: "patch_invalid"}, err
	}
	cmd := exec.CommandContext(ctx, "git", "apply", "--whitespace=error", "-")
	cmd.Dir, cmd.Stdin = root, bytes.NewReader(patch)
	output, err := collect(cmd, limit)
	if err != nil {
		output.ErrorCode = "patch_apply_failed"
	}
	return output, err
}

func replaceFile(root, relative, expected string, content []byte) (Output, error) {
	const maxContent = 56 << 10
	if len(content) > maxContent {
		return Output{ExitCode: -1, ErrorCode: "replacement_too_large"}, fmt.Errorf("replacement exceeds %d bytes", maxContent)
	}
	if len(expected) != len("sha256:")+64 || !strings.HasPrefix(expected, "sha256:") {
		return Output{ExitCode: -1, ErrorCode: "invalid_expected_hash"}, errors.New("expected_sha256 must be a sha256 artifact-style digest")
	}
	path, err := resolveExisting(root, relative)
	if err != nil {
		return Output{ExitCode: -1, ErrorCode: "invalid_path"}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Output{ExitCode: -1, ErrorCode: "read_failed"}, err
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	if copyErr != nil {
		return Output{ExitCode: -1, ErrorCode: "read_failed"}, copyErr
	}
	if closeErr != nil {
		return Output{ExitCode: -1, ErrorCode: "read_failed"}, closeErr
	}
	actual := "sha256:" + hex.EncodeToString(digest.Sum(nil))
	if actual != expected {
		return Output{ExitCode: -1, ErrorCode: "stale_file", SHA256: actual}, fmt.Errorf("file hash changed: expected %s, current %s", expected, actual)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Output{ExitCode: -1, ErrorCode: "read_failed"}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".midgard-replace-*")
	if err != nil {
		return Output{ExitCode: -1, ErrorCode: "write_failed"}, err
	}
	temporaryName := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		cleanup()
		return Output{ExitCode: -1, ErrorCode: "write_failed"}, err
	}
	if _, err := temporary.Write(content); err != nil {
		cleanup()
		return Output{ExitCode: -1, ErrorCode: "write_failed"}, err
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return Output{ExitCode: -1, ErrorCode: "write_failed"}, err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryName)
		return Output{ExitCode: -1, ErrorCode: "write_failed"}, err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		_ = os.Remove(temporaryName)
		return Output{ExitCode: -1, ErrorCode: "write_failed"}, err
	}
	newDigest := sha256.Sum256(content)
	return Output{Stdout: "replaced " + relative, ExitCode: 0, SHA256: "sha256:" + hex.EncodeToString(newDigest[:])}, nil
}

func runArgv(ctx context.Context, root string, limit int, argv ...string) (Output, error) {
	if len(argv) == 0 || strings.ContainsRune(argv[0], filepath.Separator) {
		return Output{}, errors.New("executable must be a bare name")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	return collect(cmd, limit)
}

func collect(cmd *exec.Cmd, limit int) (Output, error) {
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = limit, limit
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return Output{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode(err), Truncated: stdout.truncated || stderr.truncated}, err
}

type cappedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		p, b.truncated = p[:remaining], true
	}
	_, _ = b.Buffer.Write(p)
	return original, nil
}

func bounded(data []byte, limit int) string {
	if len(data) > limit {
		data = data[:limit]
	}
	return string(data)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}
