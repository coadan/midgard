package agentloop

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"midgard/internal/action"
	contextview "midgard/internal/context"
	runtimeenv "midgard/internal/environment"
	"midgard/internal/eventlog"
	"midgard/internal/observe"
	"midgard/internal/policy"
	"midgard/internal/provider"
	"midgard/internal/session"
	"midgard/internal/workspace"
)

type fakeSkillCatalog struct {
	read     bool
	searched bool
}

func (c *fakeSkillCatalog) Summaries() []policy.Skill {
	return []policy.Skill{{Name: "heimdal", Description: "Browser QA with compact evidence."}}
}

func (c *fakeSkillCatalog) Search(raw json.RawMessage) (json.RawMessage, error) {
	c.searched = true
	return json.RawMessage(`{"query":"browser","matches":[{"Name":"heimdal","Description":"Browser QA with compact evidence."}]}`), nil
}

func (c *fakeSkillCatalog) Read(raw json.RawMessage) (json.RawMessage, error) {
	c.read = true
	return json.RawMessage(`{"name":"heimdal","resource":"SKILL.md","content":"instructions"}`), nil
}

func TestSkillReadUsesDurableActionBoundary(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-1", "use heimdal"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StartTurn(ctx, "session-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: Capabilities()}
	catalog := &fakeSkillCatalog{}
	coordinator := Coordinator{Actions: actions, Skills: catalog}
	raw, err := coordinator.executeTool(ctx, "session-1", "turn-1", providerCall("skill", "skill_read", `{"name":"heimdal"}`))
	if err != nil || !catalog.read || !strings.Contains(string(raw), "instructions") {
		t.Fatalf("skill result = %s, read=%v, err=%v", raw, catalog.read, err)
	}
	projection, err := actions.Get(ctx, "session-1:turn-1:skill")
	if err != nil || projection.State != action.StateSucceeded || projection.Capability != "skill.read" {
		t.Fatalf("action = %#v, %v", projection, err)
	}
}

func TestInvalidSkillReadIsRetractedAndReturnedForModelRepair(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-1", "use heimdal"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StartTurn(ctx, "session-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: Capabilities()}
	var activities []Activity
	coordinator := Coordinator{Actions: actions, Skills: &fakeSkillCatalog{}, Activity: ActivityFunc(func(activity Activity) {
		activities = append(activities, activity)
	})}
	raw, err := coordinator.executeTool(ctx, "session-1", "turn-1", providerCall("skill", "skill_read", `{}`))
	if err != nil || !strings.Contains(string(raw), `"executed":false`) || !strings.Contains(string(raw), "arguments.name") {
		t.Fatalf("repair result = %s, err=%v", raw, err)
	}
	projection, err := actions.Get(ctx, "session-1:turn-1:skill")
	if err != nil || projection.State != action.StateRetracted {
		t.Fatalf("action = %#v, %v", projection, err)
	}
	if len(activities) != 2 || activities[1].State != "invalid" {
		t.Fatalf("activities = %#v", activities)
	}
}

func TestSkillSearchUsesDurableActionBoundary(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-search", "find browser instructions"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StartTurn(ctx, "session-search", "turn-search"); err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: Capabilities()}
	catalog := &fakeSkillCatalog{}
	coordinator := Coordinator{Actions: actions, Skills: catalog}
	raw, err := coordinator.executeTool(ctx, "session-search", "turn-search", providerCall("catalog", "skill_search", `{"query":"browser"}`))
	if err != nil || !catalog.searched || !strings.Contains(string(raw), "heimdal") {
		t.Fatalf("search result = %s, searched=%v, err=%v", raw, catalog.searched, err)
	}
	projection, err := actions.Get(ctx, "session-search:turn-search:catalog")
	if err != nil || projection.State != action.StateSucceeded || projection.Capability != "skill.search" {
		t.Fatalf("action = %#v, %v", projection, err)
	}
}

func TestEnvironmentDescribeUsesDurableActionWithoutExposingValues(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-environment", "inspect environment"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StartTurn(ctx, "session-environment", "turn-environment"); err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: Capabilities()}
	coordinator := Coordinator{Actions: actions, Environment: &runtimeenv.Snapshot{Name: "local", Variables: []runtimeenv.Variable{
		{Name: "PLAIN_VALUE", Value: "visible-only-to-process"},
		{Name: "SECRET_VALUE", Secret: true, SecretAccount: "keyring-account"},
	}}}
	raw, err := coordinator.executeTool(ctx, "session-environment", "turn-environment", providerCall("environment", "environment_describe", `{}`))
	if err != nil || !strings.Contains(string(raw), "PLAIN_VALUE") || !strings.Contains(string(raw), "SECRET_VALUE") || strings.Contains(string(raw), "visible-only-to-process") || strings.Contains(string(raw), "keyring-account") {
		t.Fatalf("environment result = %s, %v", raw, err)
	}
	projection, err := actions.Get(ctx, "session-environment:turn-environment:environment")
	if err != nil || projection.State != action.StateSucceeded || projection.Capability != "environment.describe" {
		t.Fatalf("action = %#v, %v", projection, err)
	}
}

func TestSystemPromptUsesOneCompactGeneralToolSurface(t *testing.T) {
	catalog := &fakeSkillCatalog{}
	coordinator := Coordinator{Skills: catalog, Context: contextview.View{Guidance: []contextview.Guidance{{Path: "AGENTS.md"}}}}
	for _, fixture := range []struct {
		name      string
		objective string
	}{
		{name: "direct advice", objective: "tell me one high impact thing we can do next"},
		{name: "bounded repository research", objective: "find where session state is assembled"},
		{name: "safe source edit", objective: "add a priority field to todos"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			prompt, err := coordinator.systemPrompt(fixture.objective)
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{
				"TOOLS", "skill_search(query)", "repo_search(query[, path])", "browser_run(command)",
				"otherwise the action is retracted for repair", "environment_describe", "file_replace(path, expected_sha256, content)",
				"an uninspected edit is retracted for repair", "EXAMPLE TOOL ACTION", "+ @read tool",
				"+ @read.arguments.path \"README.md\"", "! @read", "Never use XML/DSML or `<…tool_calls>` markup.", "An action is a small block:", "The header is always `+ @id tool`", "REPOSITORY INSTRUCTIONS", "repository/AGENTS.md", "FINISH",
			} {
				if !strings.Contains(prompt, expected) {
					t.Fatalf("prompt missing %q", expected)
				}
			}
			for _, unexpected := range []string{
				"Browser QA with compact evidence.", "Read each selected SKILL.md completely", "For an unknown repository location", "MODE:", "PROMPT MAINTENANCE", "Revisit when:",
			} {
				if strings.Contains(prompt, unexpected) {
					t.Fatalf("prompt retained eager detail %q", unexpected)
				}
			}
			if strings.Contains(prompt, "{{") || len(prompt) > maxSystemPromptBytes {
				t.Fatalf("prompt rendering = %d bytes", len(prompt))
			}
		})
	}
}

func TestSystemPromptMaintenanceRecordsExplainDurableRulesWithoutRendering(t *testing.T) {
	for _, expected := range []string{
		"PROMPT MAINTENANCE", "Rule: Emit only committed Bragi records.",
		"Rule: Never emit provider-native XML/DSML tool markup.",
		"Rule: Tool action headers use the literal entity type `tool`.",
		"Why: eagerly injected references consume context and distract direct answers.",
		"Evidence: routing recovery tests retract uninspected edits before execution.",
		"Revisit when: the secret boundary changes under an accepted security decision.",
	} {
		if !strings.Contains(systemPromptSource, expected) {
			t.Fatalf("prompt source missing maintenance rationale %q", expected)
		}
	}
	coordinator := Coordinator{}
	prompt, err := coordinator.systemPrompt("explain the repository")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "PROMPT MAINTENANCE") || strings.Contains(prompt, "Revisit when:") {
		t.Fatalf("maintenance comments leaked into rendered prompt: %q", prompt)
	}
}

func TestDirectAdvisoryObjectiveIsBoundedToRecommendations(t *testing.T) {
	for _, objective := range []string{
		"tell me one high impact thing we can do next",
		"What should we prioritize?",
		"recommend the next step",
	} {
		if !isDirectAdvisoryObjective(objective) {
			t.Fatalf("%q was not classified as direct advice", objective)
		}
	}
	for _, objective := range []string{
		"implement the next highest-impact fix",
		"explain what this project does",
		"add a priority field to todos",
	} {
		if isDirectAdvisoryObjective(objective) {
			t.Fatalf("%q was incorrectly classified as direct advice", objective)
		}
	}
}

func TestDirectInformationalObjectiveIsBoundedToQuestions(t *testing.T) {
	for _, objective := range []string{
		"whats this repo about",
		"What is this project about?",
		"how does this package work?",
	} {
		if !isDirectInformationalObjective(objective) {
			t.Fatalf("%q was not classified as direct informational", objective)
		}
	}
	for _, objective := range []string{
		"what should we build next?",
		"build durable persistence next",
		"fix the repository",
	} {
		if isDirectInformationalObjective(objective) {
			t.Fatalf("%q was incorrectly classified as direct informational", objective)
		}
	}
}

func TestSourceEditsRequireTheRootRepositoryInstructionsToBeRead(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("test instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "todo.go"), []byte("package todo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, observe.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-guidance", "edit todo"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StartTurn(ctx, "session-guidance", "turn-guidance"); err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: Capabilities()}
	coordinator := Coordinator{
		Actions: actions, Observe: observe.Service{Log: store},
		Context: contextview.View{Guidance: []contextview.Guidance{{Path: "AGENTS.md"}}},
		Runner:  workspace.Runner{Actions: &actions, Binding: workspace.Binding{SessionID: "session-guidance", WorktreeRoot: root}, Unsafe: workspace.UnsafeHostExecutor{}},
	}
	blocked, err := coordinator.executeTool(ctx, "session-guidance", "turn-guidance", providerCall("replace-before-guidance", "file_replace", `{"path":"todo.go","expected_sha256":"sha256:missing","content":"package todo\n"}`))
	if err != nil || !strings.Contains(string(blocked), `"error_code":"repository_guidance_required"`) {
		t.Fatalf("unguided edit = %s, %v", blocked, err)
	}
	if _, err := coordinator.executeTool(ctx, "session-guidance", "turn-guidance", providerCall("read-guidance", "file_inspect", `{"path":"AGENTS.md"}`)); err != nil {
		t.Fatal(err)
	}
	after, err := coordinator.executeTool(ctx, "session-guidance", "turn-guidance", providerCall("replace-after-guidance", "file_replace", `{"path":"todo.go","expected_sha256":"sha256:missing","content":"package todo\n"}`))
	if err != nil || strings.Contains(string(after), `"error_code":"repository_guidance_required"`) {
		t.Fatalf("guided edit = %s, %v", after, err)
	}
}

func TestBrowserRunRequiresHeimdalSkillBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, observe.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-browser", "check the page"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StartTurn(ctx, "session-browser", "turn-browser"); err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: Capabilities()}
	trueBinary, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	coordinator := Coordinator{
		Actions: actions, Skills: &fakeSkillCatalog{}, Observe: observe.Service{Log: store},
		Runner: workspace.Runner{Actions: &actions, Binding: workspace.Binding{SessionID: "session-browser", WorktreeRoot: t.TempDir()}, HeimdalBinary: trueBinary},
	}
	before, err := coordinator.executeTool(ctx, "session-browser", "turn-browser", providerCall("browser-before", "browser_run", `{"command":"doctor --json"}`))
	if err != nil || !strings.Contains(string(before), `"error_code":"skill_required"`) || !strings.Contains(string(before), `"executed":false`) {
		t.Fatalf("browser prerequisite result = %s, %v", before, err)
	}
	projection, err := actions.Get(ctx, "session-browser:turn-browser:browser-before")
	if err != nil || projection.State != action.StateRetracted {
		t.Fatalf("unread browser action = %#v, %v", projection, err)
	}
	evidence, err := (observe.Service{Log: store}).Evidence(ctx, "session-browser")
	if err != nil || len(evidence) != 1 || evidence[0].Kind != "tool.routing_recovery" || !strings.Contains(string(evidence[0].Payload), `"skill_required"`) {
		t.Fatalf("routing evidence = %#v, %v", evidence, err)
	}
	if _, err := coordinator.executeTool(ctx, "session-browser", "turn-browser", providerCall("read-heimdal", "skill_read", `{"name":"heimdal"}`)); err != nil {
		t.Fatal(err)
	}
	after, err := coordinator.executeTool(ctx, "session-browser", "turn-browser", providerCall("browser-after", "browser_run", `{"command":"doctor --json"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), `"error_code":"skill_required"`) {
		t.Fatalf("browser remained blocked after the skill read: %s", after)
	}
	projection, err = actions.Get(ctx, "session-browser:turn-browser:browser-after")
	if err != nil || projection.State != action.StateSucceeded {
		t.Fatalf("read browser action = %#v, %v", projection, err)
	}
}

func TestBroadShellDiscoveryGetsOneSearchRecoveryPerTurn(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, observe.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-search", "find the entry point"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StartTurn(ctx, "session-search", "turn-search"); err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: Capabilities()}
	coordinator := Coordinator{
		Actions: actions, Observe: observe.Service{Log: store},
		Runner: workspace.Runner{Actions: &actions, Binding: workspace.Binding{SessionID: "session-search", WorktreeRoot: t.TempDir()}, Unsafe: workspace.UnsafeHostExecutor{}},
	}
	first, err := coordinator.executeTool(ctx, "session-search", "turn-search", providerCall("discover-first", "shell", `{"command":"find . -maxdepth 1"}`))
	if err != nil || !strings.Contains(string(first), `"error_code":"prefer_repo_search"`) || !strings.Contains(string(first), `"executed":false`) {
		t.Fatalf("first discovery result = %s, %v", first, err)
	}
	projection, err := actions.Get(ctx, "session-search:turn-search:discover-first")
	if err != nil || projection.State != action.StateRetracted {
		t.Fatalf("first discovery action = %#v, %v", projection, err)
	}
	second, err := coordinator.executeTool(ctx, "session-search", "turn-search", providerCall("discover-second", "shell", `{"command":"find . -maxdepth 1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(second), `"error_code":"prefer_repo_search"`) {
		t.Fatalf("second discovery was corrected again: %s", second)
	}
	projection, err = actions.Get(ctx, "session-search:turn-search:discover-second")
	if err != nil || projection.State != action.StateSucceeded {
		t.Fatalf("second discovery action = %#v, %v", projection, err)
	}
}

func TestSkillReadValidationRejectsUnboundedOrEscapingReferences(t *testing.T) {
	capabilities := Capabilities()
	for _, raw := range []string{
		`{"name":"heimdal","resource":"references/browser.md"}`,
		`{"name":"heimdal","resource":"../outside.md","start_line":1,"line_count":10}`,
		`{"name":"heimdal","resource":"references/browser.md","start_line":1,"line_count":121}`,
	} {
		if err := capabilities.Validate("skill.read", json.RawMessage(raw)); err == nil {
			t.Fatalf("unsafe skill read validated: %s", raw)
		}
	}
	if err := capabilities.Validate("skill.read", json.RawMessage(`{"name":"heimdal","query":"visual evidence"}`)); err != nil {
		t.Fatalf("bounded search rejected: %v", err)
	}
}

func TestSkillSearchValidationIsBounded(t *testing.T) {
	if err := Capabilities().Validate("skill.search", json.RawMessage(`{"query":"browser QA"}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{}`, `{"query":""}`, `{"query":"` + strings.Repeat("x", 501) + `"}`} {
		if err := Capabilities().Validate("skill.search", json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid skill search validated: %s", raw)
		}
	}
}

func TestRepositorySearchValidationIsBoundedToTheRepository(t *testing.T) {
	capabilities := Capabilities()
	for _, raw := range []string{
		`{}`,
		`{"query":"needle","path":"../outside"}`,
		`{"query":"needle","path":"/tmp/outside"}`,
	} {
		if err := capabilities.Validate("repo.search", json.RawMessage(raw)); err == nil {
			t.Fatalf("unsafe repository search validated: %s", raw)
		}
	}
	if err := capabilities.Validate("repo.search", json.RawMessage(`{"query":"needle","path":"internal"}`)); err != nil {
		t.Fatalf("bounded repository search rejected: %v", err)
	}
}

func TestBrowserRunValidationKeepsHeimdalInTheCurrentWorktree(t *testing.T) {
	capabilities := Capabilities()
	for _, raw := range []string{
		`{"command":"install agent-cli"}`,
		`{"command":"doctor --dir ../outside"}`,
		`{"command":"session start --url \"http://127.0.0.1:3000\"","timeout_seconds":601}`,
		`{"command":"session start --url \"unterminated"}`,
	} {
		if err := capabilities.Validate("browser.run", json.RawMessage(raw)); err == nil {
			t.Fatalf("unsafe browser action validated: %s", raw)
		}
	}
	if err := capabilities.Validate("browser.run", json.RawMessage(`{"command":"session diagnose --json","timeout_seconds":30}`)); err != nil {
		t.Fatalf("bundled browser action rejected: %v", err)
	}
}

func TestShellValidationSeparatesForegroundTimeoutsFromBackgroundJobs(t *testing.T) {
	capabilities := Capabilities()
	for _, raw := range []string{
		`{"command":"sleep 1","timeout_seconds":601}`,
		`{"command":"sleep 1","timeout_seconds":10,"background":true}`,
	} {
		if err := capabilities.Validate("shell", json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid shell action validated: %s", raw)
		}
	}
	if err := capabilities.Validate("shell", json.RawMessage(`{"command":"server","background":true}`)); err != nil {
		t.Fatalf("background shell rejected: %v", err)
	}
}

func providerCall(id, name, arguments string) provider.ToolCall {
	return provider.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(arguments)}
}
