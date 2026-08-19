package local_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"midgard/internal/action"
	"midgard/internal/agentloop"
	runtimeenv "midgard/internal/environment"
	"midgard/internal/local"
	"midgard/internal/policy"
	"midgard/internal/project"
	"midgard/internal/provider"
)

type localSkillCatalog struct{ summaries []policy.Skill }

func (c localSkillCatalog) Summaries() []policy.Skill { return c.summaries }
func (localSkillCatalog) Search(json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"matches":[]}`), nil
}
func (localSkillCatalog) Read(json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

type acceptanceProvider struct {
	stops    []provider.Stop
	next     int
	requests []provider.Request
}

type acceptancePreparedCall struct {
	request provider.Event
	stop    provider.Stop
	step    int
}

func (c acceptancePreparedCall) RequestEvent() provider.Event { return c.request }
func (c acceptancePreparedCall) Execute(_ context.Context, sink provider.EventSink) (provider.Stop, error) {
	payload, _ := json.Marshal(map[string]int{"step": c.step})
	if err := sink.Emit(provider.Event{NativeKind: "chat.completion", NativeID: "acceptance", Sequence: 2, Payload: payload}); err != nil {
		return provider.Stop{}, err
	}
	return localBragiStop(c.stop), nil
}

func localBragiStop(stop provider.Stop) provider.Stop {
	var source strings.Builder
	if len(stop.Message.ToolCalls) > 0 {
		for _, call := range stop.Message.ToolCalls {
			id := "@" + strings.NewReplacer("-", "_", ":", "_").Replace(call.ID)
			fmt.Fprintf(&source, "+ %s tool\n+ %s.name %q\n", id, id, call.Name)
			var arguments map[string]any
			_ = json.Unmarshal(call.Arguments, &arguments)
			keys := make([]string, 0, len(arguments))
			for key := range arguments {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				raw, _ := json.Marshal(arguments[key])
				fmt.Fprintf(&source, "+ %s.arguments.%s %s\n", id, key, raw)
			}
			fmt.Fprintf(&source, "+ %s.reason \"test action\"\n! %s\n", id, id)
		}
	} else {
		content, _ := json.Marshal(stop.Message.Content)
		fmt.Fprintf(&source, "+ @answer message\n+ @answer.speaker \"assistant\"\n+ @answer.audience \"user\"\n+ @answer.channel \"final\"\n+ @answer.content %s\n! @answer\n+ @done completion\n+ @done.requested_outcome \"test complete\"\n! @done\n", content)
	}
	stop.Message = provider.Message{Role: "assistant", Content: source.String(), ReplayState: stop.Message.ReplayState}
	return stop
}

func (p *acceptanceProvider) Prepare(request provider.Request) (provider.PreparedCall, error) {
	p.next++
	p.requests = append(p.requests, request)
	payload, _ := json.Marshal(request)
	return acceptancePreparedCall{
		request: provider.Event{NativeKind: "chat.completion.request", NativeID: "acceptance-request", Sequence: 1, Payload: payload},
		stop:    p.stops[p.next-1], step: p.next,
	}, nil
}

type localTestSecrets map[string]string

func (s localTestSecrets) Set(account, secret string) error   { s[account] = secret; return nil }
func (s localTestSecrets) Get(account string) (string, error) { return s[account], nil }

func TestRuntimeEnvironmentIsDiscoverableInjectedAndAbsentFromEvidence(t *testing.T) {
	ctx := context.Background()
	repository := createGoRepo(t, "environmentfixture")
	projectCatalog := project.Catalog{Directory: t.TempDir()}
	implicit, mount, err := projectCatalog.Resolve(repository, "")
	if err != nil {
		t.Fatal(err)
	}
	environmentCatalog := runtimeenv.Catalog{Directory: t.TempDir()}
	secrets := localTestSecrets{}
	if _, err := environmentCatalog.Create("production", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := environmentCatalog.SetPlain("production", "PUBLIC_URL", "https://example.test", "public service URL"); err != nil {
		t.Fatal(err)
	}
	if _, err := environmentCatalog.SetSecret("production", "API_TOKEN", "production API token", "evidence-must-not-contain-this", secrets); err != nil {
		t.Fatal(err)
	}
	bindings := runtimeenv.Bindings{Path: filepath.Join(t.TempDir(), "bindings.json")}
	if err := bindings.Set(implicit.ID, "production"); err != nil {
		t.Fatal(err)
	}
	runtime, err := local.Open(ctx, local.Options{Repository: repository, ProjectID: implicit.ID, RepositoryName: mount.Name,
		Project: implicit, Catalog: projectCatalog, StatePath: t.TempDir(), APIKey: "unused", Model: "acceptance", DefaultBranch: "main",
		EnvironmentCatalog: environmentCatalog, EnvironmentBindings: bindings, EnvironmentSecrets: secrets, EnvironmentName: "production"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	sessionID, err := runtime.NewSession(ctx, "use the configured environment")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("fixture\n"))
	expected := "sha256:" + hex.EncodeToString(digest[:])
	script := &acceptanceProvider{stops: []provider.Stop{
		{Reason: "tool_calls", Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "read-env", Name: "shell", Arguments: mustJSON(map[string]any{"command": `printf '%s|%s' "$PUBLIC_URL" "$API_TOKEN"`})}}}},
		{Reason: "tool_calls", Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "edit", Name: "file_replace", Arguments: mustJSON(map[string]any{"path": "README.md", "expected_sha256": expected, "content": "environment used\n"})}}}},
		{Reason: "stop", Message: provider.Message{Role: "assistant", Content: "Used the configured environment."}},
	}}
	runtime.Provider = script
	result, err := runtime.RunTurn(ctx, sessionID, "use the configured environment", nil)
	if err != nil || !result.Decision.Complete {
		t.Fatalf("turn = %#v, %v", result, err)
	}
	if len(script.requests) == 0 || strings.Contains(script.requests[0].Messages[0].Content, "API_TOKEN") || strings.Contains(script.requests[0].Messages[0].Content, "PUBLIC_URL") || strings.Contains(script.requests[0].Messages[0].Content, "evidence-must-not-contain-this") {
		t.Fatalf("system context = %#v", script.requests)
	}
	var actionResult []byte
	if err := runtime.Log.DB().QueryRowContext(ctx, `SELECT result_json FROM action_projection WHERE action_id LIKE '%:read_env'`).Scan(&actionResult); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(actionResult), "[REDACTED:API_TOKEN]") || strings.Contains(string(actionResult), "evidence-must-not-contain-this") {
		t.Fatalf("action result = %s", actionResult)
	}
	stateFiles := artifactFiles(t, runtime.StatePath)
	for _, path := range stateFiles {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "evidence-must-not-contain-this") {
			t.Fatalf("secret leaked to %s", path)
		}
	}
}

func artifactFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestMultiRepositoryTurnAcceptanceEditsAndVerifiesBothWorktrees(t *testing.T) {
	ctx := context.Background()
	first := createGoRepo(t, "first")
	second := createGoRepo(t, "second")
	catalog := project.Catalog{Directory: t.TempDir()}
	implicit, firstMount, err := catalog.Resolve(first, "")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := local.Open(ctx, local.Options{Repository: first, ProjectID: implicit.ID, RepositoryName: firstMount.Name,
		Project: implicit, Catalog: catalog, StatePath: t.TempDir(), APIKey: "unused", Model: "acceptance", DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	sessionID, err := runtime.NewSession(ctx, "update both repository readmes")
	if err != nil {
		t.Fatal(err)
	}
	secondMount, err := runtime.AddRepository(ctx, sessionID, second, "multi-repository-acceptance")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("fixture\n"))
	expected := "sha256:" + hex.EncodeToString(digest[:])
	runtime.Provider = &acceptanceProvider{stops: []provider.Stop{
		{Reason: "tool_calls", Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "edit-first", Name: "file_replace", Arguments: mustJSON(map[string]any{"repository": firstMount.Name, "path": "README.md", "expected_sha256": expected, "content": "first updated\n"})}}}},
		{Reason: "tool_calls", Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "edit-second", Name: "file_replace", Arguments: mustJSON(map[string]any{"repository": secondMount.Name, "path": "README.md", "expected_sha256": expected, "content": "second updated\n"})}}}},
		{Reason: "stop", Message: provider.Message{Role: "assistant", Content: "Updated and checked both repositories."}},
	}}
	result, err := runtime.RunTurn(ctx, sessionID, "update both repository readmes", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Decision.Complete || !strings.Contains(result.Diff, "Repository: "+firstMount.Name) || !strings.Contains(result.Diff, "Repository: "+secondMount.Name) {
		t.Fatalf("multi-repository result = %#v", result)
	}
	bindings, err := runtime.Workspaces.List(ctx, sessionID)
	if err != nil || len(bindings) != 2 {
		t.Fatalf("bindings = %#v, %v", bindings, err)
	}
	want := map[string]string{firstMount.Name: "first updated\n", secondMount.Name: "second updated\n"}
	for _, binding := range bindings {
		content, err := os.ReadFile(filepath.Join(binding.WorktreeRoot, "README.md"))
		if err != nil || string(content) != want[binding.RepositoryName] {
			t.Fatalf("%s README = %q, %v", binding.RepositoryName, content, err)
		}
	}
	review, err := runtime.Review(ctx, sessionID)
	if err != nil || !review.Complete || len(review.Checks) != 2 {
		t.Fatalf("review = %#v, %v", review, err)
	}
}

func TestAddRepositoryUpgradesImplicitProjectAndBindsOnlyAtSafeBoundary(t *testing.T) {
	ctx := context.Background()
	first := createRepo(t)
	second := createRepo(t)
	catalog := project.Catalog{Directory: t.TempDir()}
	implicit, mount, err := catalog.Resolve(first, "")
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	runtime, err := local.Open(ctx, local.Options{Repository: first, ProjectID: implicit.ID, RepositoryName: mount.Name,
		Project: implicit, Catalog: catalog, StatePath: state, APIKey: "unused", Model: "deepseek-v4-pro", DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	sessionID, err := runtime.NewSession(ctx, "change both repositories")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Sessions.StartTurn(ctx, sessionID, "turn-active"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AddRepository(ctx, sessionID, second, "midgard-development"); err == nil || !strings.Contains(err.Error(), "still working") {
		t.Fatalf("active-turn add error = %v", err)
	}
	bindings, err := runtime.Workspaces.List(ctx, sessionID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings before safe boundary = %#v, %v", bindings, err)
	}
	if _, err := runtime.Sessions.EndTurn(ctx, sessionID, "turn-active", "completed"); err != nil {
		t.Fatal(err)
	}
	added, err := runtime.AddRepository(ctx, sessionID, second, "midgard-development")
	if err != nil {
		t.Fatal(err)
	}
	bindings, err = runtime.Workspaces.List(ctx, sessionID)
	if err != nil || len(bindings) != 2 || added.Name == "" {
		t.Fatalf("bindings after safe boundary = %#v, added %#v, %v", bindings, added, err)
	}
	projects, err := catalog.List()
	if err != nil || len(projects) != 1 || projects[0].ID != implicit.ID || projects[0].StatePath != state {
		t.Fatalf("upgraded project = %#v, %v", projects, err)
	}
}

func TestProjectSkillAvailabilityIsReversible(t *testing.T) {
	runtime := &local.Runtime{
		ProjectID: "project-a",
		Skills: localSkillCatalog{summaries: []policy.Skill{
			{Name: "heimdal", Description: "browser QA"},
			{Name: "midgard", Description: "runtime operations"},
		}},
		SkillMasks: project.SkillMasks{Path: filepath.Join(t.TempDir(), "skill-masks.json")},
	}
	statuses, err := runtime.SetSkillEnabled("midgard", false)
	if err != nil || len(statuses) != 2 || statuses[1].Name != "midgard" || statuses[1].Enabled {
		t.Fatalf("disabled statuses = %#v, %v", statuses, err)
	}
	statuses, err = runtime.SetSkillEnabled("midgard", true)
	if err != nil || !statuses[1].Enabled {
		t.Fatalf("enabled statuses = %#v, %v", statuses, err)
	}
}

func TestRecoverFencesLostDispatchedActionWithoutRerunningIt(t *testing.T) {
	ctx := context.Background()
	repo := createRepo(t)
	runtime, err := local.Open(ctx, local.Options{Repository: repo, StatePath: t.TempDir(), APIKey: "unused", Model: "deepseek-v4-pro", MaxProviderCalls: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	sessionID, err := runtime.NewSession(ctx, "test recovery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Sessions.StartTurn(ctx, sessionID, "turn-1"); err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: runtime.Log, Validator: agentloop.Capabilities()}
	if _, err := actions.Intent(ctx, sessionID, "action-1", "shell", json.RawMessage(`{"command":"touch must-not-exist"}`), false); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Validate(ctx, "action-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Commit(ctx, "action-1", "key-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Dispatch(ctx, "action-1", "lost-worker"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Recover(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	current, err := actions.Get(ctx, "action-1")
	if err != nil || current.State != action.StateFailed || !strings.Contains(string(current.Result), "worker_lost_outcome_unknown") {
		t.Fatalf("recovered action = %#v, %v", current, err)
	}
	binding, err := runtime.Binding(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(binding.WorktreeRoot, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("recovery reran command: %v", err)
	}
	if turnID, err := runtime.Sessions.ActiveTurn(ctx, sessionID); err != nil || turnID != "" {
		t.Fatalf("active turn after recovery = %q, %v", turnID, err)
	}
	interruptions, err := runtime.Interruptions(ctx, sessionID)
	if err != nil || len(interruptions) != 1 || !interruptions[0].UnknownOutcome {
		t.Fatalf("recovery interruption notice = %#v, %v", interruptions, err)
	}
}

func createRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run(t, repo, "git", "init", "-b", "main")
	run(t, repo, "git", "config", "user.email", "test@example.com")
	run(t, repo, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", "README.md")
	run(t, repo, "git", "commit", "-m", "initial")
	return repo
}

func createGoRepo(t *testing.T, module string) string {
	t.Helper()
	repo := t.TempDir()
	run(t, repo, "git", "init", "-b", "main")
	run(t, repo, "git", "config", "user.email", "test@example.com")
	run(t, repo, "git", "config", "user.name", "Test")
	files := map[string]string{
		"go.mod":       "module " + module + "\n\ngo 1.25.0\n",
		"main.go":      "package " + module + "\n\nfunc Name() string { return \"" + module + "\" }\n",
		"main_test.go": "package " + module + "\n\nimport \"testing\"\n\nfunc TestName(t *testing.T) { if Name() == \"\" { t.Fatal(\"empty\") } }\n",
		"README.md":    "fixture\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-m", "initial")
	return repo
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func run(t *testing.T, directory string, argv ...string) {
	t.Helper()
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v: %s", argv, err, output)
	}
}
