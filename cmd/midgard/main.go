package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"midgard/internal/action"
	"midgard/internal/agentloop"
	"midgard/internal/artifact"
	"midgard/internal/bundle"
	appconfig "midgard/internal/config"
	contextview "midgard/internal/context"
	"midgard/internal/credential"
	runtimeenv "midgard/internal/environment"
	"midgard/internal/eval"
	"midgard/internal/eventlog"
	"midgard/internal/local"
	"midgard/internal/observe"
	"midgard/internal/policy/featuredelivery"
	"midgard/internal/project"
	"midgard/internal/provider"
	"midgard/internal/retrieval"
	"midgard/internal/session"
	"midgard/internal/tui"
	"midgard/internal/workspace"

	"golang.org/x/term"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			err = initialize(ctx, os.Args[2:])
		case "rebuild":
			err = rebuild(ctx, os.Args[2:])
		case "auth":
			err = runAuth(os.Args[2:])
		case "config":
			err = runConfig(os.Args[2:])
		case "env":
			err = runEnvironment(os.Args[2:])
		case "project":
			err = runProject(os.Args[2:])
		case "skills":
			err = runSkills(os.Args[2:])
		case "search":
			err = runSearch(os.Args[2:])
		case "protocol-score":
			err = scoreProtocol(os.Args[2:])
		case "help", "-h", "--help":
			printUsage()
			return
		default:
			err = runAgent(ctx, os.Args[1:])
		}
	} else {
		err = runAgent(ctx, nil)
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func runAgent(ctx context.Context, args []string) error {
	repositoryHint, err := repositoryFromArgs(args)
	if err != nil {
		return err
	}
	catalog, err := project.OpenCatalog()
	if err != nil {
		return err
	}
	selectedProject, selectedRepository, err := resolveProject(catalog, repositoryHint, optionFromArgs(args, "project"))
	if err != nil {
		return err
	}
	repositoryHint = selectedRepository.Path
	profileHint := optionFromArgs(args, "profile")
	settings, err := appconfig.Load(repositoryHint, profileHint)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("midgard", flag.ContinueOnError)
	repository := flags.String("repo", ".", "Git repository to work in")
	projectName := flags.String("project", "", "logical Midgard project (defaults to the repository's selected project)")
	statePath := flags.String("state", "", "state directory (defaults to per-repository application data)")
	providerName := flags.String("provider", settings.Provider, "model provider")
	profile := flags.String("profile", settings.Profile, "credential profile for the provider")
	taskFlag := flags.String("task", "", "coding objective; remaining positional arguments are also accepted")
	baseURL := flags.String("deepseek-base-url", settings.BaseURL, "DeepSeek API base URL")
	defaultBranch := flags.String("default-branch", settings.DefaultBranch, "repository default branch used for landing guidance")
	landingStrategy := flags.String("landing-strategy", settings.LandingStrategy, "landing strategy: direct or pull-request")
	cleanupLanded := flags.Bool("cleanup-when-landed", settings.CleanupLanded, "remove terminal worktrees after verified landing")
	model := flags.String("model", settings.Model, "DeepSeek model")
	thinking := flags.Bool("thinking", settings.Thinking, "enable DeepSeek thinking mode")
	maxTokens := flags.Int("max-tokens", settings.MaxTokens, "maximum provider output tokens per request")
	maxProviderCalls := flags.Int("max-provider-calls", settings.MaxProviderCalls, "maximum DeepSeek requests per turn")
	headless := flags.Bool("headless", false, "run one turn with text output instead of the interactive TUI")
	if err := flags.Parse(args); err != nil {
		return err
	}
	explicitLandingStrategy, explicitProvider := false, false
	flags.Visit(func(current *flag.Flag) {
		switch current.Name {
		case "landing-strategy":
			explicitLandingStrategy = true
		case "provider":
			explicitProvider = true
		}
	})
	objective := strings.TrimSpace(*taskFlag)
	if objective == "" {
		objective = strings.TrimSpace(strings.Join(flags.Args(), " "))
	}
	if objective == "" && *headless {
		fmt.Fprint(os.Stdout, "Task: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
			return fmt.Errorf("read task: %w", err)
		}
		objective = strings.TrimSpace(line)
	}
	if objective == "" && *headless {
		return errors.New("task is required")
	}
	repo, err := filepath.Abs(*repository)
	if err != nil {
		return err
	}
	repo, err = filepath.EvalSymlinks(repo)
	if err != nil {
		return err
	}
	selectedProject, selectedRepository, err = resolveParsedProject(catalog, repo, *projectName, selectedProject, selectedRepository)
	if err != nil {
		return err
	}
	repo = selectedRepository.Path
	if _, err := workspace.InspectRepository(ctx, repo, *defaultBranch); err != nil {
		return err
	}
	if !explicitProvider && !settings.ProviderFromGit && term.IsTerminal(int(os.Stdin.Fd())) {
		selected, err := promptCodingProvider(*providerName)
		if err != nil {
			return err
		}
		if err := appconfig.SetRepositoryProvider(repo, selected); err != nil {
			return err
		}
		*providerName = selected
		fmt.Fprintf(os.Stdout, "Stored coding provider %q in the repository's local Git config.\n", selected)
	}
	if *landingStrategy != "direct" && *landingStrategy != "pull-request" {
		return fmt.Errorf("landing strategy must be %q or %q", "direct", "pull-request")
	}
	if !explicitLandingStrategy && !settings.LandingStrategyFromGit && term.IsTerminal(int(os.Stdin.Fd())) {
		selected, err := promptLandingStrategy(*landingStrategy, *defaultBranch)
		if err != nil {
			return err
		}
		if err := appconfig.SetRepositoryLandingStrategy(repo, selected); err != nil {
			return err
		}
		*landingStrategy = selected
		fmt.Fprintf(os.Stdout, "Stored landing strategy %q in the repository's local Git config.\n", selected)
	}
	providerSpec, err := lookupProvider(*providerName)
	if err != nil {
		return err
	}
	credentialStore := credential.NewStore()
	apiKey := ""
	if providerSpec.RequiredCredential != "" {
		apiKey, err = credentialStore.Get(credential.Ref{Provider: providerSpec.Name, Profile: *profile, Name: providerSpec.RequiredCredential})
		if err != nil && !errors.Is(err, credential.ErrNotFound) {
			return err
		}
	}
	if providerSpec.RequiredCredential != "" && apiKey == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("no %s configured for %s profile %q; run `midgard auth login %s --profile %s --credential %s`", providerSpec.RequiredCredential, providerSpec.Name, *profile, providerSpec.Name, *profile, providerSpec.RequiredCredential)
		}
		fmt.Fprintf(os.Stdout, "%s profile %q needs %s. It will be stored in the OS keyring.\n", providerSpec.DisplayName, *profile, providerSpec.RequiredCredential)
		apiKey, err = readAuthSecret(nil)
		if err != nil {
			return err
		}
		ref := credential.Ref{Provider: providerSpec.Name, Profile: *profile, Name: providerSpec.RequiredCredential}
		if err := credentialStore.Set(ref, apiKey); err != nil {
			return err
		}
		index, indexErr := credential.NewIndex()
		if indexErr != nil {
			return indexErr
		}
		if err := index.Add(credential.Mount{Provider: providerSpec.Name, Profile: *profile, Credential: providerSpec.RequiredCredential}); err != nil {
			return fmt.Errorf("credential was stored, but its non-secret mount could not be indexed: %w", err)
		}
	}
	yggConfiguration, yggSemantic, yggEmbeddingAPIKey, err := yggSearchSettings(credentialStore)
	if err != nil {
		return err
	}
	state := *statePath
	if state == "" {
		if selectedProject.StatePath != "" {
			state = selectedProject.StatePath
		} else if selectedProject.Implicit {
			state, err = defaultStatePath(repo)
		} else {
			state, err = projectStatePath(selectedProject.ID)
		}
		if err != nil {
			return err
		}
	}
	state, err = filepath.Abs(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	yggBinary := bundledYggBinary()
	heimdalBinary := bundledHeimdalBinary()
	environmentCatalog, err := runtimeenv.OpenCatalog()
	if err != nil {
		return err
	}
	environmentBindings, err := runtimeenv.OpenBindings()
	if err != nil {
		return err
	}
	environmentName, err := environmentBindings.Get(selectedProject.ID)
	if err != nil {
		return err
	}
	skills, err := featuredelivery.DiscoverInstalledSkills(repo)
	if err != nil {
		return fmt.Errorf("discover installed skills: %w", err)
	}
	skillMasks, err := project.OpenSkillMasks()
	if err != nil {
		return fmt.Errorf("open project skill settings: %w", err)
	}
	skillGroups, err := project.OpenSkillGroups()
	if err != nil {
		return fmt.Errorf("open skill groups: %w", err)
	}
	groupAssignments, err := skillGroups.Groups()
	if err != nil {
		return fmt.Errorf("read skill groups: %w", err)
	}
	skills = skills.WithGroups(groupAssignments)
	disabledSkills, err := skillMasks.Disabled(selectedProject.ID)
	if err != nil {
		return fmt.Errorf("read project skill settings: %w", err)
	}
	disabledGroups, err := skillGroups.Disabled(selectedProject.ID)
	if err != nil {
		return err
	}
	for _, group := range disabledGroups {
		disabledSkills = append(disabledSkills, groupAssignments[group]...)
	}
	availableSkills := featuredelivery.MaskSkills(skills, disabledSkills)
	if !*headless {
		runtime, err := local.Open(ctx, local.Options{
			Repository: repo, ProjectID: selectedProject.ID, RepositoryName: selectedRepository.Name,
			Project: selectedProject, Catalog: catalog,
			EnvironmentCatalog: environmentCatalog, EnvironmentBindings: environmentBindings,
			EnvironmentSecrets: runtimeenv.NativeSecretStore{}, EnvironmentName: environmentName,
			StatePath: state, APIKey: apiKey, BaseURL: *baseURL, ProviderName: *providerName, Profile: *profile, Credentials: credentialStore,
			Model: *model, ThinkingEnabled: *thinking, MaxTokens: *maxTokens,
			MaxProviderCalls: *maxProviderCalls, DefaultBranch: *defaultBranch,
			LandingStrategy: *landingStrategy, CleanupLanded: *cleanupLanded,
			Skills: skills, SkillMasks: skillMasks, SkillGroups: skillGroups,
			YggBinary: yggBinary, YggConfiguration: yggConfiguration, YggSemantic: yggSemantic, YggEmbeddingAPIKey: yggEmbeddingAPIKey, HeimdalBinary: heimdalBinary,
		})
		if err != nil {
			return err
		}
		defer runtime.Close()
		return tui.Run(ctx, runtime, objective)
	}
	artifacts, err := artifact.Open(filepath.Join(state, "artifacts"))
	if err != nil {
		return err
	}
	log, err := openLog(ctx, filepath.Join(state, "state.sqlite"))
	if err != nil {
		return err
	}
	defer log.Close()
	sessions := session.Service{Log: log}
	sessionID := randomID("session")
	if _, err := sessions.CreateInProject(ctx, sessionID, selectedProject.ID, objective); err != nil {
		return err
	}
	workspaces := workspace.Service{Log: log, WorktreeBase: filepath.Join(state, "worktrees"), DefaultBranch: *defaultBranch,
		LandingStrategy: *landingStrategy, CleanupWhenLanded: *cleanupLanded,
		ProjectID: selectedProject.ID, RepositoryName: selectedRepository.Name}
	binding, err := workspaces.Bind(ctx, sessionID, repo)
	if err != nil {
		return err
	}
	configuration, err := (featuredelivery.Policy{}).Configure(objective, repo)
	if err != nil {
		return err
	}
	actions := action.Service{Log: log, Validator: agentloop.Capabilities()}
	var environmentSnapshot *runtimeenv.Snapshot
	var environmentResolver workspace.EnvironmentResolver
	if environmentName != "" {
		snapshot, err := environmentCatalog.Snapshot(environmentName)
		if err != nil {
			return err
		}
		environmentSnapshot = &snapshot
		environmentResolver = runtimeenv.Resolver{Snapshot: snapshot, Secrets: runtimeenv.NativeSecretStore{}}
	}
	view, err := (contextview.Assembler{Log: log}).Build(ctx, objective, binding)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Midgard private prototype\nUNSAFE: commands run directly on this host without containment or approval.\nSession: %s\nWorktree: %s\nModel: %s\n\n", sessionID, binding.WorktreeRoot, *model)
	backgroundJobs := &workspace.BackgroundJobs{}
	defer backgroundJobs.Close()
	selectedProvider := provider.Provider(provider.DeepSeek{APIKey: apiKey, BaseURL: *baseURL, Model: *model, ThinkingEnabled: *thinking, MaxTokens: *maxTokens})
	if providerSpec.Name == "codex" {
		effort := "standard"
		if *thinking {
			effort = "high"
		}
		selectedProvider = provider.Codex{Model: *model, Effort: effort}
	}
	coordinator := agentloop.Coordinator{
		Provider:  selectedProvider,
		Artifacts: artifacts, Sessions: sessions, Actions: actions, Observe: observe.Service{Log: log},
		// Action results are canonical event payloads in this first slice, so keep
		// both output streams comfortably below the 64 KiB envelope limit.
		Runner: workspace.Runner{
			Actions: &actions, Binding: binding, Unsafe: workspace.UnsafeHostExecutor{}, Environment: environmentResolver, Jobs: backgroundJobs,
			MaxOutput: min(configuration.Budget.MaxOutputBytes, 24<<10), YggBinary: yggBinary, YggStorageRoot: filepath.Join(state, "ygg", bundle.YggdrasilVersion),
			YggConfiguration: yggConfiguration, YggSemantic: yggSemantic, YggEmbeddingAPIKey: yggEmbeddingAPIKey, HeimdalBinary: heimdalBinary,
		},
		Context: view, Configuration: configuration, Environment: environmentSnapshot, Skills: availableSkills,
		Policy: featuredelivery.Policy{}, Activity: &agentloop.TextActivitySink{Writer: os.Stdout},
		MaxProviderCalls: *maxProviderCalls,
	}
	result, err := coordinator.Run(ctx, sessionID, objective)
	if err != nil {
		if ctx.Err() != nil {
			_, _ = sessions.Cancel(context.Background(), sessionID, "local interrupt")
		}
		return fmt.Errorf("session %s failed (worktree retained at %s): %w", sessionID, binding.WorktreeRoot, err)
	}
	cleaned, cleanupErr := workspaces.CleanupIfLanded(ctx, sessionID)
	if cleanupErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: completed session but could not reconcile landed worktree: %v\n", cleanupErr)
	}
	if cleaned {
		fmt.Fprintf(os.Stdout, "\nCompleted with %d provider calls and %d durable actions.\nLanded worktree cleaned from %s\n", result.ProviderCalls, result.Actions, result.Worktree)
		return nil
	}
	fmt.Fprintf(os.Stdout, "\nCompleted with %d provider calls and %d durable actions.\nWorktree retained at %s\n", result.ProviderCalls, result.Actions, result.Worktree)
	return nil
}

func resolveParsedProject(catalog project.Catalog, repository, requested string, selected project.Project, mount project.Repository) (project.Project, project.Repository, error) {
	if strings.TrimSpace(requested) == "" {
		return selected, mount, nil
	}
	return catalog.Resolve(repository, requested)
}

func repositoryFromArgs(args []string) (string, error) {
	repository := "."
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "-repo" || argument == "--repo" {
			if index+1 >= len(args) {
				return "", errors.New("-repo requires a path")
			}
			repository = args[index+1]
			index++
			continue
		}
		if value, ok := strings.CutPrefix(argument, "-repo="); ok {
			repository = value
		} else if value, ok := strings.CutPrefix(argument, "--repo="); ok {
			repository = value
		}
	}
	absolute, err := filepath.Abs(repository)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func resolveProject(catalog project.Catalog, repository, requested string) (project.Project, project.Repository, error) {
	selected, mount, err := catalog.Resolve(repository, requested)
	var choice *project.ChoiceRequiredError
	if !errors.As(err, &choice) || !term.IsTerminal(int(os.Stdin.Fd())) {
		return selected, mount, err
	}
	fmt.Fprintf(os.Stdout, "This repository belongs to more than one Midgard project. Which project are you working in?\n")
	for index, candidate := range choice.Projects {
		fmt.Fprintf(os.Stdout, "  %d. %s\n", index+1, candidate.Name)
	}
	fmt.Fprint(os.Stdout, "Choose a project: ")
	line, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return project.Project{}, project.Repository{}, readErr
	}
	value := strings.TrimSpace(line)
	for index, candidate := range choice.Projects {
		if value != fmt.Sprintf("%d", index+1) && value != candidate.Name {
			continue
		}
		selected, mount, err = catalog.Resolve(repository, candidate.ID)
		if err == nil {
			err = project.Remember(repository, candidate.ID)
		}
		return selected, mount, err
	}
	return project.Project{}, project.Repository{}, errors.New("choose one of the listed projects")
}

type repositoryArgs []string

func (values *repositoryArgs) String() string { return strings.Join(*values, ",") }
func (values *repositoryArgs) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func runSkills(args []string) error {
	groups, err := project.OpenSkillGroups()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Println("usage: midgard skills groups | group set GROUP SKILL... | group clear GROUP | group enable|disable GROUP [-repo PATH] [-project NAME]")
		return nil
	}
	if args[0] == "groups" {
		values, err := groups.Groups()
		if err != nil {
			return err
		}
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			fmt.Println("No skill groups are configured.")
			return nil
		}
		for _, name := range names {
			fmt.Printf("%s\n", name)
			for _, skill := range values[name] {
				fmt.Printf("  %s\n", skill)
			}
		}
		return nil
	}
	if args[0] != "group" || len(args) < 3 {
		return errors.New("Midgard expected `skills group set|clear|enable|disable GROUP`")
	}
	action, group := args[1], args[2]
	switch action {
	case "set":
		if len(args) < 4 {
			return errors.New("include at least one installed skill name")
		}
		if err := groups.Assign(group, args[3:]); err != nil {
			return err
		}
		fmt.Printf("Assigned %d skills to group %s.\n", len(args)-3, group)
		return nil
	case "clear":
		if len(args) != 3 {
			return errors.New("usage: midgard skills group clear GROUP")
		}
		if err := groups.Clear(group); err != nil {
			return err
		}
		fmt.Printf("Removed skill group %s.\n", group)
		return nil
	case "enable", "disable":
		flags := flag.NewFlagSet("skills group "+action, flag.ContinueOnError)
		repository := flags.String("repo", ".", "repository whose project skill catalog should change")
		projectName := flags.String("project", "", "logical Midgard project")
		if err := flags.Parse(args[3:]); err != nil {
			return err
		}
		catalog, err := project.OpenCatalog()
		if err != nil {
			return err
		}
		selected, _, err := catalog.Resolve(*repository, *projectName)
		if err != nil {
			return err
		}
		if err := groups.SetEnabled(selected.ID, group, action == "enable"); err != nil {
			return err
		}
		fmt.Printf("Skill group %s is now %s for project %s.\n", group, map[bool]string{true: "available", false: "hidden"}[action == "enable"], selected.Name)
		return nil
	default:
		return errors.New("Midgard expected set, clear, enable, or disable")
	}
}

func runProject(args []string) error {
	catalog, err := project.OpenCatalog()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printProjectUsage()
		return nil
	}
	switch args[0] {
	case "list":
		projects, err := catalog.List()
		if err != nil {
			return err
		}
		if len(projects) == 0 {
			fmt.Println("No named Midgard projects yet. Repositories still work as implicit one-repository projects.")
			return nil
		}
		for _, current := range projects {
			fmt.Printf("%s (%s)\n", current.Name, current.ID)
			for _, repository := range current.Repositories {
				fmt.Printf("  %s  %s\n", repository.Name, repository.Path)
			}
		}
		return nil
	case "create":
		if len(args) < 2 {
			return errors.New("project name is required")
		}
		flags := flag.NewFlagSet("project create", flag.ContinueOnError)
		var rawRepositories repositoryArgs
		flags.Var(&rawRepositories, "repo", "repository mount as NAME=PATH; repeat for multiple repositories")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		repositories, err := parseRepositoryArgs(rawRepositories)
		if err != nil {
			return err
		}
		created, err := catalog.Create(args[1], repositories)
		if err != nil {
			return err
		}
		fmt.Printf("Created project %s (%s) with %d repositories.\n", created.Name, created.ID, len(created.Repositories))
		return nil
	case "add-repo":
		if len(args) != 4 {
			return errors.New("usage: midgard project add-repo PROJECT NAME PATH")
		}
		updated, err := catalog.AddRepository(args[1], project.Repository{Name: args[2], Path: args[3]})
		if err != nil {
			return err
		}
		fmt.Printf("Added %s to project %s.\n", args[2], updated.Name)
		return nil
	case "upgrade":
		flags := flag.NewFlagSet("project upgrade", flag.ContinueOnError)
		repository := flags.String("repo", ".", "current repository to upgrade")
		addName := flags.String("add-name", "", "mount name for an additional repository")
		addPath := flags.String("add-path", "", "path to an additional repository")
		if len(args) < 2 {
			return errors.New("project name is required")
		}
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		implicit, _, err := catalog.Resolve(*repository, "")
		if err != nil {
			return err
		}
		implicit.StatePath, err = defaultStatePath(implicit.Repositories[0].Path)
		if err != nil {
			return err
		}
		var additional *project.Repository
		if *addName != "" || *addPath != "" {
			if *addName == "" || *addPath == "" {
				return errors.New("-add-name and -add-path must be supplied together")
			}
			additional = &project.Repository{Name: *addName, Path: *addPath}
		}
		upgraded, err := catalog.Upgrade(implicit, args[1], additional)
		if err != nil {
			return err
		}
		for _, repository := range upgraded.Repositories {
			_ = project.Remember(repository.Path, upgraded.ID)
		}
		fmt.Printf("Upgraded to project %s with %d repositories.\n", upgraded.Name, len(upgraded.Repositories))
		return nil
	case "use":
		if len(args) < 2 {
			return errors.New("project name is required")
		}
		flags := flag.NewFlagSet("project use", flag.ContinueOnError)
		repository := flags.String("repo", ".", "repository whose default project should change")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		selected, _, err := catalog.Resolve(*repository, args[1])
		if err != nil {
			return err
		}
		if err := project.Remember(*repository, selected.ID); err != nil {
			return err
		}
		fmt.Printf("This repository will now default to project %s.\n", selected.Name)
		return nil
	default:
		return fmt.Errorf("unknown project command %q", args[0])
	}
}

func runEnvironment(args []string) error {
	catalog, err := runtimeenv.OpenCatalog()
	if err != nil {
		return err
	}
	bindings, err := runtimeenv.OpenBindings()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printEnvironmentUsage()
		return nil
	}
	switch args[0] {
	case "list":
		environments, err := catalog.List()
		if err != nil {
			return err
		}
		if len(environments) == 0 {
			fmt.Println("No Midgard environments yet. Create one with `midgard env create NAME`.")
			return nil
		}
		for _, current := range environments {
			fmt.Printf("%s  revision %d  %d variables\n", current.Name, current.Revision, len(current.Variables))
		}
		return nil
	case "create":
		if len(args) < 2 {
			return errors.New("environment name is required")
		}
		flags := flag.NewFlagSet("env create", flag.ContinueOnError)
		parent := flags.String("parent", "", "environment whose current values this environment extends")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		created, err := catalog.Create(args[1], *parent)
		if err != nil {
			return err
		}
		fmt.Printf("Created environment %s.\n", created.Name)
		return nil
	case "set":
		if len(args) < 4 {
			return errors.New("usage: midgard env set ENVIRONMENT KEY VALUE [--description TEXT]")
		}
		flags := flag.NewFlagSet("env set", flag.ContinueOnError)
		description := flags.String("description", "", "agent-visible purpose of this variable")
		if err := flags.Parse(args[4:]); err != nil {
			return err
		}
		updated, err := catalog.SetPlain(args[1], args[2], args[3], *description)
		if err != nil {
			return err
		}
		fmt.Printf("Stored %s in environment %s revision %d.\n", strings.ToUpper(args[2]), updated.Name, updated.Revision)
		return nil
	case "set-secret":
		if len(args) < 3 {
			return errors.New("usage: midgard env set-secret ENVIRONMENT KEY [--from-env NAME] [--description TEXT]")
		}
		flags := flag.NewFlagSet("env set-secret", flag.ContinueOnError)
		fromEnvironment := flags.String("from-env", "", "import the value from this process environment variable")
		description := flags.String("description", "", "agent-visible purpose of this variable")
		if err := flags.Parse(args[3:]); err != nil {
			return err
		}
		var source *string
		if *fromEnvironment != "" {
			source = fromEnvironment
		}
		secret, err := readAuthSecret(source)
		if err != nil {
			return err
		}
		updated, err := catalog.SetSecret(args[1], args[2], *description, secret, runtimeenv.NativeSecretStore{})
		if err != nil {
			return err
		}
		fmt.Printf("Stored %s in the OS keyring for environment %s revision %d. Its value cannot be displayed by Midgard.\n", strings.ToUpper(args[2]), updated.Name, updated.Revision)
		return nil
	case "unset":
		if len(args) != 3 {
			return errors.New("usage: midgard env unset ENVIRONMENT KEY")
		}
		updated, err := catalog.Unset(args[1], args[2])
		if err != nil {
			return err
		}
		fmt.Printf("Removed %s from environment %s revision %d. Prior immutable revisions remain available for recovery.\n", strings.ToUpper(args[2]), updated.Name, updated.Revision)
		return nil
	case "use":
		if len(args) < 2 {
			return errors.New("environment name is required")
		}
		flags := flag.NewFlagSet("env use", flag.ContinueOnError)
		repository := flags.String("repo", ".", "repository whose project should use this environment")
		projectName := flags.String("project", "", "logical Midgard project to bind")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if _, err := catalog.Current(args[1]); err != nil {
			return err
		}
		projectCatalog, err := project.OpenCatalog()
		if err != nil {
			return err
		}
		selected, _, err := projectCatalog.Resolve(*repository, *projectName)
		if err != nil {
			return err
		}
		if err := bindings.Set(selected.ID, args[1]); err != nil {
			return err
		}
		fmt.Printf("Project %s will use environment %s for later turns.\n", selected.Name, strings.ToLower(args[1]))
		return nil
	case "status":
		name, repository, requestedProject, err := parseEnvironmentStatusArgs(args[1:])
		if err != nil {
			return err
		}
		if name == "" {
			projectCatalog, err := project.OpenCatalog()
			if err != nil {
				return err
			}
			selected, _, err := projectCatalog.Resolve(repository, requestedProject)
			if err != nil {
				return err
			}
			name, err = bindings.Get(selected.ID)
			if err != nil {
				return err
			}
			if name == "" {
				fmt.Printf("Project %s has no runtime environment. Use `midgard env use NAME`.\n", selected.Name)
				return nil
			}
		}
		snapshot, err := catalog.Snapshot(name)
		if err != nil {
			return err
		}
		return writeEnvironmentStatus(os.Stdout, snapshot)
	default:
		return fmt.Errorf("unknown environment command %q", args[0])
	}
}

func writeEnvironmentStatus(writer io.Writer, snapshot runtimeenv.Snapshot) error {
	if _, err := fmt.Fprintf(writer, "Environment %s  %s\n", snapshot.Name, snapshot.ID); err != nil {
		return err
	}
	for _, variable := range snapshot.Inspect() {
		if _, err := fmt.Fprintf(writer, "  %s  %s  %s", variable.Name, variable.Kind, variable.State); err != nil {
			return err
		}
		if variable.SourceEnvironment != "" {
			if _, err := fmt.Fprintf(writer, "  from %s revision %d", variable.SourceEnvironment, variable.SourceRevision); err != nil {
				return err
			}
			if variable.Inherited {
				if _, err := fmt.Fprint(writer, " (inherited)"); err != nil {
					return err
				}
			}
		}
		if variable.Description != "" {
			if _, err := fmt.Fprintf(writer, "  — %s", variable.Description); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(writer); err != nil {
			return err
		}
	}
	return nil
}

func parseEnvironmentStatusArgs(args []string) (name, repository, projectName string, err error) {
	repository = "."
	for index := 0; index < len(args); index++ {
		switch argument := args[index]; {
		case argument == "-repo" || argument == "--repo":
			if index+1 >= len(args) {
				return "", "", "", errors.New("-repo requires a path")
			}
			repository = args[index+1]
			index++
		case strings.HasPrefix(argument, "-repo="):
			repository = strings.TrimPrefix(argument, "-repo=")
		case strings.HasPrefix(argument, "--repo="):
			repository = strings.TrimPrefix(argument, "--repo=")
		case argument == "-project" || argument == "--project":
			if index+1 >= len(args) {
				return "", "", "", errors.New("-project requires a name")
			}
			projectName = args[index+1]
			index++
		case strings.HasPrefix(argument, "-project="):
			projectName = strings.TrimPrefix(argument, "-project=")
		case strings.HasPrefix(argument, "--project="):
			projectName = strings.TrimPrefix(argument, "--project=")
		case strings.HasPrefix(argument, "-"):
			return "", "", "", fmt.Errorf("unknown env status option %q", argument)
		case name == "":
			name = argument
		default:
			return "", "", "", errors.New("env status accepts at most one environment name")
		}
	}
	return name, repository, projectName, nil
}

func printEnvironmentUsage() {
	fmt.Println(`usage:
  midgard env list
  midgard env create NAME [--parent NAME]
  midgard env set ENVIRONMENT KEY VALUE [--description TEXT]
  midgard env set-secret ENVIRONMENT KEY [--from-env NAME] [--description TEXT]
  midgard env unset ENVIRONMENT KEY
  midgard env use NAME [-repo PATH] [-project NAME]
  midgard env status [NAME] [-repo PATH] [-project NAME]`)
}

func parseRepositoryArgs(values []string) ([]project.Repository, error) {
	if len(values) == 0 {
		return nil, errors.New("at least one -repo NAME=PATH is required")
	}
	repositories := make([]project.Repository, 0, len(values))
	for _, value := range values {
		name, path, ok := strings.Cut(value, "=")
		if !ok || name == "" || path == "" {
			return nil, fmt.Errorf("invalid repository mount %q; expected NAME=PATH", value)
		}
		repositories = append(repositories, project.Repository{Name: name, Path: path})
	}
	return repositories, nil
}

func printProjectUsage() {
	fmt.Println(`usage:
  midgard project list
  midgard project create NAME -repo NAME=PATH [-repo NAME=PATH ...]
  midgard project add-repo PROJECT NAME PATH
  midgard project upgrade NAME [-repo PATH] [-add-name NAME -add-path PATH]
  midgard project use PROJECT [-repo PATH]`)
}

func optionFromArgs(args []string, name string) string {
	short, long := "-"+name, "--"+name
	for index, argument := range args {
		if (argument == short || argument == long) && index+1 < len(args) {
			return args[index+1]
		}
		if value, ok := strings.CutPrefix(argument, short+"="); ok {
			return value
		}
		if value, ok := strings.CutPrefix(argument, long+"="); ok {
			return value
		}
	}
	return ""
}

func promptLandingStrategy(defaultValue, defaultBranch string) (string, error) {
	defaultChoice := "2"
	if defaultValue == "pull-request" {
		defaultChoice = "1"
	}
	fmt.Fprintf(os.Stdout, `How do changes normally land in this repository?
  1. Through a pull request on a remote Git forge.
  2. I work locally, merge directly into %s, and push.
Choose 1 or 2 (default %s): `, defaultBranch, defaultChoice)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read landing strategy: %w", err)
	}
	value := strings.ToLower(strings.TrimSpace(line))
	if value == "" {
		return defaultValue, nil
	}
	if value == "1" || value == "pr" {
		value = "pull-request"
	} else if value == "2" || value == "local" {
		value = "direct"
	}
	if value != "direct" && value != "pull-request" {
		return "", errors.New("landing strategy must be direct or pull-request")
	}
	return value, nil
}

func promptCodingProvider(defaultValue string) (string, error) {
	installed := provider.Installed()
	if len(installed) == 0 {
		return "", errors.New("no coding provider adapters are installed")
	}
	mounts := []credential.Mount{}
	if index, err := credential.NewIndex(); err == nil {
		mounts, _ = index.List()
	}
	defaultChoice := 1
	fmt.Fprintln(os.Stdout, "Which coding provider should Midgard use for this repository?")
	for index, definition := range installed {
		if definition.Name == defaultValue {
			defaultChoice = index + 1
		}
		var profiles []string
		for _, mount := range mounts {
			if mount.Provider == definition.Name && mount.Credential == definition.RequiredCredential {
				profiles = append(profiles, mount.Profile)
			}
		}
		credentialState := "no configured mounts"
		if len(profiles) > 0 {
			credentialState = "configured profiles: " + strings.Join(profiles, ", ")
		}
		fmt.Fprintf(os.Stdout, "  %d. %s (%s)\n", index+1, definition.DisplayName, credentialState)
	}
	fmt.Fprintf(os.Stdout, "Choose a provider (default %d): ", defaultChoice)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read coding provider: %w", err)
	}
	value := strings.ToLower(strings.TrimSpace(line))
	if value == "" {
		return installed[defaultChoice-1].Name, nil
	}
	for index, definition := range installed {
		if value == fmt.Sprintf("%d", index+1) || value == definition.Name {
			return definition.Name, nil
		}
	}
	return "", fmt.Errorf("choose one of the installed providers (1-%d)", len(installed))
}

func runConfig(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Println("usage: midgard config show [-repo PATH] [-profile NAME]")
		return nil
	}
	if args[0] != "show" {
		return fmt.Errorf("unknown config command %q; use show", args[0])
	}
	flags := flag.NewFlagSet("config show", flag.ContinueOnError)
	repository := flags.String("repo", ".", "repository whose overrides should be loaded")
	profile := flags.String("profile", "", "profile whose layered settings should be loaded")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	repo, err := filepath.Abs(*repository)
	if err != nil {
		return err
	}
	repo, err = filepath.EvalSymlinks(repo)
	if err != nil {
		return err
	}
	result, err := appconfig.Load(repo, *profile)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func lookupProvider(name string) (provider.Definition, error) {
	return provider.Lookup(name)
}

func runSearch(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Println(`usage:
  midgard search status
  midgard search embeddings enable --endpoint URL --model NAME --dimensions COUNT --provider NAME [--profile NAME] [--credential NAME]
  midgard search embeddings disable

Semantic repository search is optional. The endpoint and model are stored in
Midgard configuration; the referenced API key stays in the OS keyring. Set the
key first with: midgard auth login PROVIDER --profile NAME`)
		return nil
	}
	catalog, err := retrieval.OpenCatalog()
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return errors.New("usage: midgard search status")
		}
		settings, err := catalog.Load()
		if err != nil {
			return err
		}
		if settings.Embedding == nil {
			fmt.Println("Semantic repository search is off. Midgard uses local lexical search.")
			return nil
		}
		ref := settings.Embedding.CredentialRef()
		ready, err := credential.NewStore().Exists(ref)
		if err != nil {
			return err
		}
		state := "ready"
		if !ready {
			state = "missing from OS keyring; Midgard will use lexical search"
		}
		fmt.Printf("Semantic repository search: %s\n  %s\n  %s\n  credential %s/%s: %s\n", state, settings.Embedding.Model, settings.Embedding.Endpoint, ref.Provider, ref.Profile, state)
		return nil
	case "embeddings":
		if len(args) < 2 {
			return errors.New("choose `midgard search embeddings enable` or `midgard search embeddings disable`")
		}
		switch args[1] {
		case "disable":
			if len(args) != 2 {
				return errors.New("usage: midgard search embeddings disable")
			}
			if err := catalog.DisableEmbedding(); err != nil {
				return err
			}
			fmt.Println("Semantic repository search is off. Midgard will use local lexical search.")
			return nil
		case "enable":
			flags := flag.NewFlagSet("search embeddings enable", flag.ContinueOnError)
			endpoint := flags.String("endpoint", "", "OpenAI-compatible embeddings endpoint")
			model := flags.String("model", "", "embedding model")
			dimensions := flags.Int("dimensions", 0, "embedding vector dimensions")
			providerName := flags.String("provider", "", "Midgard credential provider holding the API key")
			profile := flags.String("profile", credential.DefaultProfile, "Midgard credential profile")
			credentialName := flags.String("credential", credential.APIKey, "Midgard credential name")
			timeout := flags.Int("timeout-ms", 15_000, "embedding request timeout in milliseconds")
			batchSize := flags.Int("batch-size", 0, "embedding batch size; provider default when omitted")
			maxInput := flags.Int("max-input-chars", 0, "maximum source characters per embedding; provider default when omitted")
			queryPrefix := flags.String("query-prefix", "", "optional text prepended to search queries")
			documentPrefix := flags.String("document-prefix", "", "optional text prepended to indexed documents")
			if err := flags.Parse(args[2:]); err != nil {
				return err
			}
			if flags.NArg() != 0 {
				return errors.New("embedding setup accepts flags only")
			}
			embedding := retrieval.Embedding{Endpoint: *endpoint, Model: *model, Dimensions: *dimensions, Provider: *providerName, Profile: *profile, Credential: *credentialName, TimeoutMS: *timeout, BatchSize: *batchSize, MaxInputChars: *maxInput, QueryPrefix: *queryPrefix, DocumentPrefix: *documentPrefix}
			if err := embedding.Validate(); err != nil {
				return err
			}
			ready, err := credential.NewStore().Exists(embedding.CredentialRef())
			if err != nil {
				return err
			}
			if !ready {
				return fmt.Errorf("Midgard needs an API key for %s profile %q before semantic search can be enabled; run `midgard auth login %s --profile %s --credential %s`", embedding.Provider, embedding.Profile, embedding.Provider, embedding.Profile, embedding.Credential)
			}
			if err := catalog.SetEmbedding(embedding); err != nil {
				return err
			}
			fmt.Printf("Semantic repository search is ready with %s. Midgard will pass its key directly to bundled Yggdrasil when search runs.\n", embedding.Model)
			return nil
		default:
			return errors.New("choose `midgard search embeddings enable` or `midgard search embeddings disable`")
		}
	default:
		return errors.New("use `midgard search status` or `midgard search embeddings enable|disable`")
	}
}

func runAuth(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printAuthUsage()
		return nil
	}
	action := args[0]
	if action == "list" {
		return listAuthMounts()
	}
	flags := flag.NewFlagSet("auth "+action, flag.ContinueOnError)
	profile := flags.String("profile", credential.DefaultProfile, "named credential profile")
	credentialName := flags.String("credential", credential.APIKey, "credential kind, such as api-key or token")
	var fromEnv *string
	if action == "login" {
		fromEnv = flags.String("from-env", "", "copy the API key from this environment variable")
	}
	providerName, flagArgs, err := splitAuthProvider(args[1:])
	if err != nil {
		return err
	}
	if err := flags.Parse(flagArgs); err != nil {
		return err
	}
	if providerName == "" && flags.NArg() == 1 {
		providerName = flags.Arg(0)
	} else if flags.NArg() != 0 {
		return errors.New("auth accepts exactly one provider name")
	}
	if providerName == "" {
		return fmt.Errorf("provider is required; run `midgard auth %s PROVIDER`", action)
	}
	ref := credential.Ref{Provider: providerName, Profile: *profile, Name: *credentialName}
	if _, err := ref.Account(); err != nil {
		return err
	}
	store := credential.NewStore()
	index, err := credential.NewIndex()
	if err != nil {
		return err
	}
	mount := credential.Mount{Provider: strings.ToLower(providerName), Profile: strings.ToLower(*profile), Credential: strings.ToLower(*credentialName)}
	switch action {
	case "login":
		secret, err := readAuthSecret(fromEnv)
		if err != nil {
			return err
		}
		if err := store.Set(ref, secret); err != nil {
			return err
		}
		if err := index.Add(mount); err != nil {
			return fmt.Errorf("credential was stored, but its non-secret mount could not be indexed: %w", err)
		}
		fmt.Printf("stored %s profile %q %s in the OS keyring\n", strings.ToLower(providerName), *profile, *credentialName)
		if *fromEnv != "" {
			fmt.Printf("future runs can omit %s\n", *fromEnv)
		}
		return nil
	case "status":
		exists, err := store.Exists(ref)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("no stored API key for %s profile %q", strings.ToLower(providerName), *profile)
		}
		fmt.Printf("%s profile %q has %s in the OS keyring\n", strings.ToLower(providerName), *profile, *credentialName)
		return nil
	case "logout":
		if err := store.Delete(ref); err != nil {
			if errors.Is(err, credential.ErrNotFound) {
				return fmt.Errorf("no stored API key for %s profile %q", strings.ToLower(providerName), *profile)
			}
			return err
		}
		if err := index.Remove(mount); err != nil {
			return fmt.Errorf("credential was removed, but its non-secret mount index could not be updated: %w", err)
		}
		fmt.Printf("removed %s profile %q %s from the OS keyring\n", strings.ToLower(providerName), *profile, *credentialName)
		return nil
	default:
		return fmt.Errorf("unknown auth command %q; use login, status, or logout", action)
	}
}

func listAuthMounts() error {
	index, err := credential.NewIndex()
	if err != nil {
		return err
	}
	mounts, err := index.List()
	if err != nil {
		return err
	}
	if len(mounts) == 0 {
		fmt.Println("No provider credentials have been configured with Midgard.")
		return nil
	}
	store := credential.NewStore()
	for _, mount := range mounts {
		ready, checkErr := store.Exists(credential.Ref{Provider: mount.Provider, Profile: mount.Profile, Name: mount.Credential})
		state := "ready"
		if checkErr != nil {
			state = "unavailable: " + checkErr.Error()
		} else if !ready {
			state = "missing from keyring"
		}
		adapter := "adapter not installed"
		if _, lookupErr := provider.Lookup(mount.Provider); lookupErr == nil {
			adapter = "adapter installed"
		}
		fmt.Printf("%s/%s  %s  %s, %s\n", mount.Provider, mount.Profile, mount.Credential, state, adapter)
	}
	return nil
}

func splitAuthProvider(args []string) (string, []string, error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:], nil
	}
	return "", args, nil
}

func readAuthSecret(fromEnv *string) (string, error) {
	if fromEnv != nil && *fromEnv != "" {
		secret := strings.TrimSpace(os.Getenv(*fromEnv))
		if secret == "" {
			return "", fmt.Errorf("environment variable %s is empty or unset", *fromEnv)
		}
		return secret, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("interactive login requires a terminal; use --from-env ENV_VAR")
	}
	fmt.Fprint(os.Stderr, "API key: ")
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read API key: %w", err)
	}
	value := strings.TrimSpace(string(secret))
	if value == "" {
		return "", errors.New("API key is empty")
	}
	return value, nil
}

func printAuthUsage() {
	fmt.Println(`usage:
  midgard auth login PROVIDER [--profile NAME] [--from-env ENV_VAR]
  midgard auth status PROVIDER [--profile NAME]
  midgard auth logout PROVIDER [--profile NAME]
  midgard auth list

Credentials are stored in the operating system keyring. Profiles mount the
same provider with independent API keys; the default profile is "default".`)
}

func initialize(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	state := flags.String("state", ".midgard", "local state directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*state, 0o700); err != nil {
		return err
	}
	if _, err := artifact.Open(filepath.Join(*state, "artifacts")); err != nil {
		return err
	}
	log, err := openLog(ctx, filepath.Join(*state, "state.sqlite"))
	if err != nil {
		return err
	}
	defer log.Close()
	fmt.Printf("initialized Midgard state at %s\n", *state)
	return nil
}

func rebuild(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("rebuild", flag.ContinueOnError)
	state := flags.String("state", ".midgard", "local state directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	log, err := openLog(ctx, filepath.Join(*state, "state.sqlite"))
	if err != nil {
		return err
	}
	defer log.Close()
	if err := log.Rebuild(ctx); err != nil {
		return err
	}
	fmt.Println("rebuilt projections from canonical events")
	return nil
}

func scoreProtocol(args []string) error {
	flags := flag.NewFlagSet("protocol-score", flag.ContinueOnError)
	manifestPath := flags.String("manifest", "testdata/protocol/manifest.json", "comparison manifest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	manifest, err := eval.LoadManifest(*manifestPath)
	if err != nil {
		return err
	}
	report, err := eval.Score(manifest)
	if err != nil {
		return err
	}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(encoded))
	return nil
}

func openLog(ctx context.Context, path string) (*eventlog.Store, error) {
	return eventlog.Open(ctx, path, session.Projector{}, action.Projector{}, workspace.Projector{}, observe.Projector{})
}

func defaultStatePath(repository string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(repository))
	return filepath.Join(base, "midgard", hex.EncodeToString(digest[:8])), nil
}

func projectStatePath(projectID string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "midgard", "project-state", projectID), nil
}

func randomID(prefix string) string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(data[:])
}

func bundledYggBinary() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	binary, err := bundle.ResolveYggdrasil(executable)
	if err != nil {
		return ""
	}
	return binary
}

func bundledHeimdalBinary() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	binary, err := bundle.ResolveHeimdal(executable)
	if err != nil {
		return ""
	}
	return binary
}

func yggSearchSettings(store credential.Store) ([]byte, bool, string, error) {
	catalog, err := retrieval.OpenCatalog()
	if err != nil {
		return nil, false, "", err
	}
	settings, err := catalog.Load()
	if err != nil {
		return nil, false, "", err
	}
	configuration, enabled, err := settings.YggConfig()
	if err != nil {
		return nil, false, "", err
	}
	if !enabled || settings.Embedding == nil {
		return configuration, false, "", nil
	}
	secret, err := store.Get(settings.Embedding.CredentialRef())
	if errors.Is(err, credential.ErrNotFound) {
		// Semantic retrieval is optional. A removed key falls back safely to
		// lexical retrieval instead of preventing the whole coding session.
		return configuration, false, "", nil
	}
	if err != nil {
		return nil, false, "", fmt.Errorf("load semantic search credential: %w", err)
	}
	return configuration, true, secret, nil
}

func printUsage() {
	fmt.Println(`usage:
  midgard [flags] ["coding objective"]
  midgard auth login PROVIDER [--profile NAME] [--from-env ENV_VAR]
  midgard auth status PROVIDER [--profile NAME]
  midgard auth logout PROVIDER [--profile NAME]
  midgard auth list
  midgard search status
  midgard search embeddings enable --endpoint URL --model NAME --dimensions COUNT --provider NAME [--profile NAME]
  midgard search embeddings disable
  midgard config show [-repo PATH] [-profile NAME]
  midgard env list
  midgard env create NAME [--parent NAME]
  midgard env set ENVIRONMENT KEY VALUE
  midgard env set-secret ENVIRONMENT KEY [--from-env NAME]
  midgard env unset ENVIRONMENT KEY
  midgard env use NAME [-repo PATH] [-project NAME]
  midgard env status [NAME] [-repo PATH] [-project NAME]
  midgard project list
  midgard project create NAME -repo NAME=PATH [-repo NAME=PATH ...]
  midgard project add-repo PROJECT NAME PATH
  midgard project upgrade NAME [-repo PATH] [-add-name NAME -add-path PATH]
  midgard project use PROJECT [-repo PATH]
  midgard skills groups
  midgard skills group set GROUP SKILL...
  midgard skills group clear GROUP
  midgard skills group enable|disable GROUP [-repo PATH] [-project NAME]
  midgard init [-state PATH]
  midgard rebuild [-state PATH]
  midgard protocol-score [-manifest PATH]

Running midgard opens the repository session TUI; -provider and -profile select
the model provider and stored credential. A coding objective starts a
new chat immediately. Pass -headless to run one text-only turn. This prototype
executes commands directly on the host.`)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "midgard: "+format+"\n", args...)
	os.Exit(1)
}
