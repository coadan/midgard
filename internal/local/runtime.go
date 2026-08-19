package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"midgard/internal/action"
	"midgard/internal/agentloop"
	"midgard/internal/artifact"
	"midgard/internal/bundle"
	contextview "midgard/internal/context"
	"midgard/internal/credential"
	runtimeenv "midgard/internal/environment"
	"midgard/internal/eventlog"
	"midgard/internal/observe"
	"midgard/internal/policy"
	"midgard/internal/policy/featuredelivery"
	"midgard/internal/project"
	"midgard/internal/provider"
	"midgard/internal/session"
	"midgard/internal/workspace"
)

type Options struct {
	Repository          string
	ProjectID           string
	RepositoryName      string
	Project             project.Project
	Catalog             project.Catalog
	EnvironmentCatalog  runtimeenv.Catalog
	EnvironmentBindings runtimeenv.Bindings
	EnvironmentSecrets  runtimeenv.SecretStore
	EnvironmentName     string
	StatePath           string
	APIKey              string
	ProviderName        string
	Profile             string
	Effort              string
	Credentials         credential.Store
	BaseURL             string
	DefaultBranch       string
	LandingStrategy     string
	CleanupLanded       bool
	Model               string
	ThinkingEnabled     bool
	MaxTokens           int
	MaxProviderCalls    int
	Skills              policy.SkillCatalog
	SkillMasks          project.SkillMasks
	SkillGroups         project.SkillGroups
	YggBinary           string
	YggConfiguration    []byte
	YggSemantic         bool
	YggEmbeddingAPIKey  string
	HeimdalBinary       string
}

type Runtime struct {
	Repository          string
	ProjectID           string
	RepositoryName      string
	Project             project.Project
	Catalog             project.Catalog
	EnvironmentCatalog  runtimeenv.Catalog
	EnvironmentBindings runtimeenv.Bindings
	EnvironmentSecrets  runtimeenv.SecretStore
	EnvironmentName     string
	StatePath           string
	Model               string
	ProviderName        string
	Profile             string
	Effort              string
	BaseURL             string
	APIKey              string
	MaxTokens           int
	Credentials         credential.Store
	MaxProviderCalls    int
	Skills              policy.SkillCatalog
	SkillMasks          project.SkillMasks
	SkillGroups         project.SkillGroups
	YggBinary           string
	YggConfiguration    []byte
	YggSemantic         bool
	YggEmbeddingAPIKey  string
	HeimdalBinary       string
	Log                 *eventlog.Store
	Artifacts           *artifact.Store
	Sessions            session.Service
	Workspaces          workspace.Service
	Provider            provider.Provider
	Jobs                *workspace.BackgroundJobs
}

type Review struct {
	Worktree string
	Diff     string
	Checks   []Check
	Complete bool
	Reasons  []string
}

type Check struct {
	Argv     []string `json:"argv"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
}

func Open(ctx context.Context, options Options) (*Runtime, error) {
	if options.Repository == "" || options.StatePath == "" {
		return nil, errors.New("repository and state path are required")
	}
	if err := os.MkdirAll(options.StatePath, 0o700); err != nil {
		return nil, err
	}
	artifacts, err := artifact.Open(filepath.Join(options.StatePath, "artifacts"))
	if err != nil {
		return nil, err
	}
	log, err := eventlog.Open(ctx, filepath.Join(options.StatePath, "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{}, observe.Projector{})
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{
		Repository: options.Repository, ProjectID: options.ProjectID, RepositoryName: options.RepositoryName, StatePath: options.StatePath, Model: options.Model,
		ProviderName: valueOr(options.ProviderName, "deepseek"), Profile: valueOr(options.Profile, credential.DefaultProfile), Effort: options.Effort,
		BaseURL: options.BaseURL, APIKey: options.APIKey, MaxTokens: options.MaxTokens, Credentials: options.Credentials,
		Project: options.Project, Catalog: options.Catalog,
		EnvironmentCatalog: options.EnvironmentCatalog, EnvironmentBindings: options.EnvironmentBindings,
		EnvironmentSecrets: options.EnvironmentSecrets, EnvironmentName: options.EnvironmentName,
		MaxProviderCalls: options.MaxProviderCalls, Skills: options.Skills, SkillMasks: options.SkillMasks, SkillGroups: options.SkillGroups,
		YggBinary: options.YggBinary, YggConfiguration: options.YggConfiguration, YggSemantic: options.YggSemantic, YggEmbeddingAPIKey: options.YggEmbeddingAPIKey, HeimdalBinary: options.HeimdalBinary,
		Log: log, Artifacts: artifacts,
	}
	if runtime.Project.ID == "" {
		runtime.Project = project.Project{Version: 1, ID: options.ProjectID, Name: options.RepositoryName, Implicit: true,
			Repositories: []project.Repository{{Name: options.RepositoryName, Path: options.Repository}}}
	}
	runtime.Sessions = session.Service{Log: log}
	runtime.Workspaces = workspace.Service{Log: log, WorktreeBase: filepath.Join(options.StatePath, "worktrees"), DefaultBranch: options.DefaultBranch,
		LandingStrategy: options.LandingStrategy, CleanupWhenLanded: options.CleanupLanded, ProjectID: options.ProjectID, RepositoryName: options.RepositoryName}
	runtime.Jobs = &workspace.BackgroundJobs{}
	if runtime.Effort == "" {
		if options.ThinkingEnabled {
			runtime.Effort = "high"
		} else {
			runtime.Effort = "standard"
		}
	}
	runtime.Provider = runtime.buildProvider(runtime.ProviderName, runtime.Profile, runtime.Model, runtime.Effort)
	return runtime, nil
}

func (r *Runtime) Close() error {
	if r.Jobs != nil {
		r.Jobs.Close()
	}
	return r.Log.Close()
}

func (r *Runtime) NewSession(ctx context.Context, objective string) (string, error) {
	sessionID := randomID("session")
	if _, err := r.Sessions.CreateInProject(ctx, sessionID, r.ProjectID, objective); err != nil {
		return "", err
	}
	if _, err := r.Workspaces.Bind(ctx, sessionID, r.Repository); err != nil {
		return "", err
	}
	if _, err := r.Sessions.SelectModel(ctx, sessionID, session.ModelSelection{Provider: r.ProviderName, Profile: r.Profile, Model: r.Model, Effort: r.Effort}); err != nil {
		return "", err
	}
	return sessionID, nil
}

func (r *Runtime) RunTurn(ctx context.Context, sessionID, objective string, sink agentloop.ActivitySink) (agentloop.Result, error) {
	selection, err := r.Sessions.ModelSelection(ctx, sessionID)
	if err != nil {
		return agentloop.Result{}, err
	}
	selectedProvider := r.Provider
	if selection.Provider != "" && (selection.Provider != r.ProviderName || selection.Profile != r.Profile || selection.Model != r.Model || selection.Effort != r.Effort) {
		selectedProvider = r.buildProvider(selection.Provider, selection.Profile, selection.Model, selection.Effort)
	}
	bindings, err := r.Workspaces.List(ctx, sessionID)
	if err != nil {
		return agentloop.Result{}, err
	}
	if len(bindings) == 0 {
		return agentloop.Result{}, fmt.Errorf("this chat has no repository worktree")
	}
	actions := action.Service{Log: r.Log, Validator: agentloop.Capabilities()}
	runners := make(map[string]workspace.Runner, len(bindings))
	views := make(map[string]contextview.View, len(bindings))
	repositoryChecks := make(map[string][][]string, len(bindings))
	var configuration policy.Configuration
	var environmentSnapshot *runtimeenv.Snapshot
	var environmentLoader workspace.EnvironmentResolver
	if r.EnvironmentName != "" {
		snapshot, err := r.EnvironmentCatalog.Snapshot(r.EnvironmentName)
		if err != nil {
			return agentloop.Result{}, fmt.Errorf("load runtime environment %s: %w", r.EnvironmentName, err)
		}
		environmentSnapshot = &snapshot
		environmentLoader = runtimeenv.Resolver{Snapshot: snapshot, Secrets: r.EnvironmentSecrets}
	}
	for _, binding := range bindings {
		currentConfiguration, err := (featuredelivery.Policy{}).Configure(objective, binding.RepositoryRoot)
		if err != nil {
			return agentloop.Result{}, err
		}
		if configuration.PolicyID == "" || binding.RepositoryName == r.RepositoryName {
			configuration = currentConfiguration
		}
		view, err := (contextview.Assembler{Log: r.Log}).Build(ctx, objective, binding)
		if err != nil {
			return agentloop.Result{}, err
		}
		views[binding.RepositoryName] = view
		repositoryChecks[binding.RepositoryName] = currentConfiguration.RequiredChecks
		runners[binding.RepositoryName] = workspace.Runner{
			Actions: &actions, Binding: binding, Unsafe: workspace.UnsafeHostExecutor{}, Environment: environmentLoader, Jobs: r.Jobs,
			MaxOutput: min(currentConfiguration.Budget.MaxOutputBytes, 24<<10), YggBinary: r.YggBinary,
			YggStorageRoot: filepath.Join(r.StatePath, "ygg", bundle.YggdrasilVersion), YggConfiguration: r.YggConfiguration,
			YggSemantic: r.YggSemantic, YggEmbeddingAPIKey: r.YggEmbeddingAPIKey, HeimdalBinary: r.HeimdalBinary,
		}
	}
	disabledSkills, err := r.SkillMasks.Disabled(r.ProjectID)
	if err != nil {
		return agentloop.Result{}, fmt.Errorf("read project skill settings: %w", err)
	}
	disabledGroups, err := r.SkillGroups.Disabled(r.ProjectID)
	if err != nil {
		return agentloop.Result{}, fmt.Errorf("read project skill groups: %w", err)
	}
	groups, err := r.SkillGroups.Groups()
	if err != nil {
		return agentloop.Result{}, err
	}
	for _, group := range disabledGroups {
		disabledSkills = append(disabledSkills, groups[group]...)
	}
	coordinator := agentloop.Coordinator{
		Provider: selectedProvider, Artifacts: r.Artifacts, Sessions: r.Sessions,
		Actions: actions, Observe: observe.Service{Log: r.Log},
		Runners: runners, Contexts: views, Configuration: configuration, RepositoryChecks: repositoryChecks,
		Environment: environmentSnapshot,
		Skills:      featuredelivery.MaskSkills(r.Skills, disabledSkills),
		Policy:      featuredelivery.Policy{}, Activity: sink,
		MaxProviderCalls: r.MaxProviderCalls,
	}
	return coordinator.RunTurn(ctx, sessionID, objective)
}

type ModelOption struct {
	Provider, ProviderName, Model, Name, Description, Effort string
	Efforts                                                  []string
	Selected                                                 bool
}

type AuthOption struct {
	Provider, Name, Profile, Detail string
	Authenticated                   bool
}

func (r *Runtime) AuthOptions(ctx context.Context) ([]AuthOption, error) {
	var options []AuthOption
	for _, definition := range provider.Installed() {
		option := AuthOption{Provider: definition.Name, Name: definition.DisplayName, Profile: r.Profile, Detail: definition.AuthDescription}
		if definition.Name == "codex" {
			ok, detail, err := provider.CodexAuthStatus(ctx)
			if err != nil {
				return nil, err
			}
			option.Authenticated, option.Detail = ok, valueOr(detail, definition.AuthDescription)
		} else {
			store := r.Credentials
			if store == (credential.Store{}) {
				store = credential.NewStore()
			}
			ok, err := store.Exists(credential.Ref{Provider: definition.Name, Profile: r.Profile, Name: definition.RequiredCredential})
			if err != nil {
				return nil, err
			}
			option.Authenticated = ok
		}
		options = append(options, option)
	}
	return options, nil
}

func (r *Runtime) ModelOptions(ctx context.Context) ([]ModelOption, error) {
	var options []ModelOption
	for _, definition := range provider.Installed() {
		models, err := provider.Models(ctx, definition.Name)
		if err != nil {
			options = append(options, ModelOption{Provider: definition.Name, ProviderName: definition.DisplayName, Name: "Unavailable", Description: err.Error()})
			continue
		}
		for _, model := range models {
			effort := model.DefaultEffort
			if definition.Name == r.ProviderName && model.ID == r.Model {
				effort = r.Effort
			}
			options = append(options, ModelOption{Provider: definition.Name, ProviderName: definition.DisplayName, Model: model.ID, Name: model.DisplayName,
				Description: model.Description, Efforts: append([]string(nil), model.Efforts...), Effort: effort,
				Selected: definition.Name == r.ProviderName && model.ID == r.Model})
		}
	}
	return options, nil
}

func (r *Runtime) SelectModel(ctx context.Context, sessionID, providerName, model, effort string) (ModelOption, error) {
	models, err := provider.Models(ctx, providerName)
	if err != nil {
		return ModelOption{}, err
	}
	var selected *provider.ModelDefinition
	for index := range models {
		if models[index].ID == model {
			selected = &models[index]
			break
		}
	}
	if selected == nil {
		return ModelOption{}, fmt.Errorf("%s is not available from %s", model, providerName)
	}
	if !slices.Contains(selected.Efforts, effort) {
		return ModelOption{}, fmt.Errorf("%s does not support %s effort", model, effort)
	}
	if providerName == "deepseek" {
		store := r.Credentials
		if store == (credential.Store{}) {
			store = credential.NewStore()
		}
		if _, err := store.Get(credential.Ref{Provider: providerName, Profile: r.Profile, Name: credential.APIKey}); err != nil {
			return ModelOption{}, fmt.Errorf("DeepSeek profile %q is not authenticated; open `/auth` to add its API key", r.Profile)
		}
	}
	if providerName == "codex" {
		loggedIn, _, err := provider.CodexAuthStatus(ctx)
		if err != nil {
			return ModelOption{}, err
		}
		if !loggedIn {
			return ModelOption{}, errors.New("Codex is not authenticated; open `/auth` and sign in")
		}
	}
	selection := session.ModelSelection{Provider: providerName, Profile: r.Profile, Model: model, Effort: effort}
	if _, err := r.Sessions.SelectModel(ctx, sessionID, selection); err != nil {
		return ModelOption{}, err
	}
	r.ProviderName, r.Model, r.Effort = providerName, model, effort
	r.Provider = r.buildProvider(providerName, r.Profile, model, effort)
	return ModelOption{Provider: providerName, Model: model, Name: selected.DisplayName, Effort: effort, Efforts: selected.Efforts, Selected: true}, nil
}

func (r *Runtime) buildProvider(providerName, profile, model, effort string) provider.Provider {
	if providerName == "codex" {
		return provider.Codex{Model: model, Effort: effort}
	}
	apiKey := r.APIKey
	if profile != r.Profile || apiKey == "" {
		store := r.Credentials
		if store == (credential.Store{}) {
			store = credential.NewStore()
		}
		apiKey, _ = store.Get(credential.Ref{Provider: "deepseek", Profile: profile, Name: credential.APIKey})
	}
	return provider.DeepSeek{APIKey: apiKey, BaseURL: r.BaseURL, Model: model, ThinkingEnabled: effort != "standard", MaxTokens: r.MaxTokens}
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

type SkillStatus struct {
	Name        string
	Description string
	Group       string
	Enabled     bool
	IsGroup     bool
	Members     int
}

type EnvironmentOption struct {
	Name      string
	Active    bool
	Variables []runtimeenv.VariableInfo
}

func (r *Runtime) Environments() ([]EnvironmentOption, error) {
	environments, err := r.EnvironmentCatalog.List()
	if err != nil {
		return nil, err
	}
	options := make([]EnvironmentOption, 0, len(environments))
	for _, environment := range environments {
		snapshot, err := r.EnvironmentCatalog.Snapshot(environment.Name)
		if err != nil {
			return nil, err
		}
		options = append(options, EnvironmentOption{Name: snapshot.Name, Active: snapshot.Name == r.EnvironmentName, Variables: snapshot.Inspect()})
	}
	return options, nil
}

func (r *Runtime) SkillStatuses() ([]SkillStatus, error) {
	disabled, err := r.SkillMasks.Disabled(r.ProjectID)
	if err != nil {
		return nil, err
	}
	masked := make(map[string]bool, len(disabled))
	for _, name := range disabled {
		masked[name] = true
	}
	disabledGroups, err := r.SkillGroups.Disabled(r.ProjectID)
	if err != nil {
		return nil, err
	}
	groupMasked := map[string]bool{}
	for _, group := range disabledGroups {
		groupMasked[group] = true
	}
	if r.Skills == nil {
		return nil, nil
	}
	summaries := r.Skills.Summaries()
	counts := map[string]int{}
	for _, summary := range summaries {
		if summary.Group != "" {
			counts[summary.Group]++
		}
	}
	groupNames := make([]string, 0, len(counts))
	for group := range counts {
		groupNames = append(groupNames, group)
	}
	slices.Sort(groupNames)
	statuses := make([]SkillStatus, 0, len(summaries)+len(groupNames))
	for _, group := range groupNames {
		statuses = append(statuses, SkillStatus{Name: group, Group: group, Description: fmt.Sprintf("%d grouped skills", counts[group]), Enabled: !groupMasked[group], IsGroup: true, Members: counts[group]})
		for _, summary := range summaries {
			if summary.Group == group {
				statuses = append(statuses, SkillStatus{Name: summary.Name, Group: group, Description: summary.Description, Enabled: !masked[summary.Name] && !groupMasked[group]})
			}
		}
	}
	for _, summary := range summaries {
		if summary.Group == "" {
			statuses = append(statuses, SkillStatus{Name: summary.Name, Description: summary.Description, Enabled: !masked[summary.Name]})
		}
	}
	return statuses, nil
}

func (r *Runtime) SetSkillEnabled(name string, enabled bool) ([]SkillStatus, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a skill name is required")
	}
	statuses, err := r.SkillStatuses()
	if err != nil {
		return nil, err
	}
	found, isGroup := false, false
	for _, status := range statuses {
		if status.Name == name {
			found, isGroup = true, status.IsGroup
		}
	}
	if !found {
		return nil, fmt.Errorf("skill %q is not installed", name)
	}
	if isGroup {
		if err := r.SkillGroups.SetEnabled(r.ProjectID, name, enabled); err != nil {
			return nil, err
		}
		return r.SkillStatuses()
	}
	disabled, err := r.SkillMasks.Disabled(r.ProjectID)
	if err != nil {
		return nil, err
	}
	masked := make(map[string]bool, len(disabled)+1)
	for _, current := range disabled {
		masked[current] = true
	}
	if enabled {
		delete(masked, name)
	} else {
		masked[name] = true
	}
	disabled = disabled[:0]
	for current := range masked {
		disabled = append(disabled, current)
	}
	if err := r.SkillMasks.Set(r.ProjectID, disabled); err != nil {
		return nil, err
	}
	return r.SkillStatuses()
}

func (r *Runtime) EnvironmentStatus() (runtimeenv.Snapshot, error) {
	if r.EnvironmentName == "" {
		return runtimeenv.Snapshot{}, errors.New("this project has no runtime environment; open `/env` and choose one")
	}
	return r.EnvironmentCatalog.Snapshot(r.EnvironmentName)
}

func (r *Runtime) UseEnvironment(ctx context.Context, sessionID, name string) (runtimeenv.Snapshot, error) {
	if sessionID != "" {
		turnID, err := r.Sessions.ActiveTurn(ctx, sessionID)
		if err != nil {
			return runtimeenv.Snapshot{}, err
		}
		if turnID != "" {
			return runtimeenv.Snapshot{}, errors.New("the agent is still working; wait for the current turn to finish before changing environments")
		}
	}
	snapshot, err := r.EnvironmentCatalog.Snapshot(name)
	if err != nil {
		return runtimeenv.Snapshot{}, err
	}
	if r.EnvironmentBindings.Path == "" {
		return runtimeenv.Snapshot{}, errors.New("environment bindings are unavailable; restart Midgard and try again")
	}
	if err := r.EnvironmentBindings.Set(r.ProjectID, snapshot.Name); err != nil {
		return runtimeenv.Snapshot{}, err
	}
	r.EnvironmentName = snapshot.Name
	return snapshot, nil
}

var ErrProjectNameRequired = errors.New("a project name is required before adding a second repository")

func (r *Runtime) PrepareRepository(ctx context.Context, path string) (project.Repository, error) {
	if _, err := workspace.InspectRepository(ctx, path, r.Workspaces.DefaultBranch); err != nil {
		return project.Repository{}, err
	}
	return project.RepositoryMount(r.Project, path)
}

// AddRepository changes catalog membership and appends workspace.bound only
// while no turn is active. This is the server-side safe-boundary fence behind
// the TUI command.
func (r *Runtime) AddRepository(ctx context.Context, sessionID, path, projectName string) (project.Repository, error) {
	turnID, err := r.Sessions.ActiveTurn(ctx, sessionID)
	if err != nil {
		return project.Repository{}, err
	}
	if turnID != "" {
		return project.Repository{}, fmt.Errorf("the agent is still working; wait for the current turn to finish before attaching a repository")
	}
	current, err := r.Sessions.Get(ctx, sessionID)
	if err != nil {
		return project.Repository{}, err
	}
	if current.ProjectID != r.ProjectID {
		return project.Repository{}, fmt.Errorf("this chat belongs to a different project")
	}
	mount, err := r.PrepareRepository(ctx, path)
	if err != nil {
		return project.Repository{}, err
	}
	bindings, err := r.Workspaces.List(ctx, sessionID)
	if err != nil {
		return project.Repository{}, err
	}
	for _, binding := range bindings {
		if binding.RepositoryRoot == mount.Path {
			return mount, nil
		}
	}
	if r.Catalog.Directory == "" {
		return project.Repository{}, errors.New("the project catalog is unavailable; restart Midgard and try again")
	}
	if r.Project.Implicit {
		if strings.TrimSpace(projectName) == "" {
			return project.Repository{}, ErrProjectNameRequired
		}
		r.Project.StatePath = r.StatePath
		r.Project, err = r.Catalog.Upgrade(r.Project, projectName, &mount)
	} else {
		r.Project, err = r.Catalog.AddRepository(r.Project.ID, mount)
	}
	if err != nil {
		return project.Repository{}, err
	}
	r.ProjectID = r.Project.ID
	service := r.Workspaces
	service.ProjectID, service.RepositoryName = r.Project.ID, mount.Name
	if _, err := service.Bind(ctx, sessionID, mount.Path); err != nil {
		return project.Repository{}, err
	}
	for _, repository := range r.Project.Repositories {
		if err := project.Remember(repository.Path, r.Project.ID); err != nil {
			return project.Repository{}, fmt.Errorf("repository attached, but Midgard could not remember the project choice for %s: %w", repository.Path, err)
		}
	}
	return mount, nil
}

func (r *Runtime) Finish(ctx context.Context, sessionID string) error {
	if _, err := r.Sessions.Finish(ctx, sessionID, "completed", "closed by local client"); err != nil {
		return err
	}
	return r.cleanupLandedBindings(ctx, sessionID)
}

func (r *Runtime) Steer(ctx context.Context, sessionID, content string) (session.Control, error) {
	return r.Sessions.Steer(ctx, sessionID, content)
}

func (r *Runtime) SessionSummaries(ctx context.Context) ([]session.Summary, error) {
	summaries, err := r.Sessions.ListByRepository(ctx, r.Repository)
	if err != nil {
		return nil, err
	}
	for _, summary := range summaries {
		if summary.Status == "active" {
			continue
		}
		if err := r.cleanupLandedBindings(ctx, summary.SessionID); err != nil {
			return nil, fmt.Errorf("reconcile landed worktree for %s: %w", summary.SessionID, err)
		}
	}
	return summaries, nil
}

func (r *Runtime) cleanupLandedBindings(ctx context.Context, sessionID string) error {
	bindings, err := r.Workspaces.List(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		service := r.Workspaces
		service.RepositoryName = binding.RepositoryName
		if _, err := service.CleanupIfLanded(ctx, sessionID); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) Messages(ctx context.Context, sessionID string) ([]session.Message, error) {
	return r.Sessions.Messages(ctx, sessionID)
}

func (r *Runtime) RecentMessages(ctx context.Context, sessionID string, byteLimit, messageLimit int) (session.TranscriptWindow, error) {
	return r.Sessions.RecentMessages(ctx, sessionID, byteLimit, messageLimit)
}

func (r *Runtime) Interruptions(ctx context.Context, sessionID string) ([]session.Interruption, error) {
	return r.Sessions.Interruptions(ctx, sessionID)
}

func (r *Runtime) RecentInterruptions(ctx context.Context, sessionID string, minSequence int64, limit int) ([]session.Interruption, error) {
	return r.Sessions.RecentInterruptions(ctx, sessionID, minSequence, limit)
}

func (r *Runtime) TurnUsages(ctx context.Context, sessionID string) ([]session.TurnUsage, error) {
	return r.Sessions.TurnUsages(ctx, sessionID)
}

func (r *Runtime) RecentTurnUsages(ctx context.Context, sessionID string, limit int) ([]session.TurnUsage, error) {
	return r.Sessions.RecentTurnUsages(ctx, sessionID, limit)
}

// ActionTimeline is the action-owned durable presentation read model exposed
// through the local runtime for interactive clients.
type ActionTimeline = action.TimelineItem

type ActionTimelineWindow = action.TimelineWindow

func (r *Runtime) RecentActions(ctx context.Context, sessionID string, minSequence int64, limit int) (ActionTimelineWindow, error) {
	return (action.Service{Log: r.Log}).RecentTimeline(ctx, sessionID, minSequence, limit)
}

func (r *Runtime) Recover(ctx context.Context, sessionID string) error {
	turnID, err := r.Sessions.ActiveTurn(ctx, sessionID)
	if err != nil {
		return err
	}
	actions := action.Service{Log: r.Log, Validator: agentloop.Capabilities()}
	nonTerminal, err := actions.NonTerminal(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, current := range nonTerminal {
		switch current.State {
		case action.StateIntent, action.StateValidated, action.StateApprovalPending:
			if _, err := actions.Retract(ctx, current.ActionID); err != nil {
				return err
			}
		case action.StateCommitted:
			claim, err := actions.Dispatch(ctx, current.ActionID, "local-recovery")
			if err != nil {
				return err
			}
			if _, err := actions.Result(ctx, claim, false, json.RawMessage(`{"error":"worker_lost_before_dispatch","executed":false,"exit_code":-1}`)); err != nil {
				return err
			}
		case action.StateDispatched:
			claim := action.Claim{ActionID: current.ActionID, CommitID: current.CommitID, Owner: current.DispatchOwner, Fence: current.DispatchFence}
			claim, err = actions.Reassign(ctx, claim, "local-recovery")
			if err != nil {
				return err
			}
			if _, err := actions.Result(ctx, claim, false, json.RawMessage(`{"error":"worker_lost_outcome_unknown","executed":"unknown","exit_code":-1}`)); err != nil {
				return err
			}
		}
	}
	if turnID != "" {
		if _, err := r.Sessions.EndTurn(ctx, sessionID, turnID, "interrupted"); err != nil {
			return err
		}
	}
	selection, err := r.Sessions.ModelSelection(ctx, sessionID)
	if err != nil {
		return err
	}
	if selection.Provider != "" {
		r.ProviderName, r.Profile, r.Model, r.Effort = selection.Provider, selection.Profile, selection.Model, selection.Effort
		r.Provider = r.buildProvider(selection.Provider, selection.Profile, selection.Model, selection.Effort)
	}
	return nil
}

func (r *Runtime) Review(ctx context.Context, sessionID string) (Review, error) {
	bindings, err := r.Workspaces.List(ctx, sessionID)
	if err != nil {
		return Review{}, err
	}
	var roots []string
	for _, binding := range bindings {
		roots = append(roots, binding.RepositoryName+": "+binding.WorktreeRoot)
	}
	review := Review{Worktree: strings.Join(roots, ", ")}
	evidence, err := (observe.Service{Log: r.Log}).Evidence(ctx, sessionID)
	if err != nil {
		return Review{}, err
	}
	for _, item := range evidence {
		switch item.Kind {
		case "git.diff":
			var value struct {
				Repository string `json:"repository"`
				Stdout     string `json:"stdout"`
			}
			if json.Unmarshal(item.Payload, &value) == nil {
				if value.Stdout != "" {
					review.Diff += fmt.Sprintf("Repository: %s\n%s", value.Repository, value.Stdout)
				}
			}
		case "check.result":
			var check Check
			if json.Unmarshal(item.Payload, &check) == nil {
				review.Checks = append(review.Checks, check)
			}
		case "completion.decision":
			var decision struct {
				Complete bool     `json:"complete"`
				Reasons  []string `json:"reasons"`
			}
			if json.Unmarshal(item.Payload, &decision) == nil {
				review.Complete, review.Reasons = decision.Complete, decision.Reasons
			}
		}
	}
	return review, nil
}

func (r *Runtime) Binding(ctx context.Context, sessionID string) (workspace.Binding, error) {
	return r.Workspaces.Get(ctx, sessionID)
}

func (r *Runtime) GitStatus(ctx context.Context, sessionID string) (string, error) {
	bindings, err := r.Workspaces.List(ctx, sessionID)
	if err != nil {
		return "", err
	}
	for _, binding := range bindings {
		command := exec.CommandContext(ctx, "git", "status", "--porcelain=v1", "--untracked-files=all")
		command.Dir = binding.WorktreeRoot
		output, err := command.Output()
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(string(output)) != "" {
			return "modified", nil
		}
	}
	return "clean", nil
}

func randomID(prefix string) string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(fmt.Sprintf("random ID: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(data[:])
}
