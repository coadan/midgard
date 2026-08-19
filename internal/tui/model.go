package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"midgard/internal/agentloop"
	"midgard/internal/local"
	"midgard/internal/provider"
	"midgard/internal/session"
)

type Mode int

const (
	// Keep source text modest, then cap the terminal-ready result below. ANSI
	// styling and soft wrapping can expand a small source transcript into many
	// thousands of terminal lines, which makes viewport scrolling sluggish.
	tuiTranscriptBytes         = 96 << 10
	tuiMessageCharacters       = 16 << 10
	tuiTranscriptRenderedLines = 1200
	tuiInterruptionLimit       = 64
	tuiTurnUsageLimit          = 128
	tuiToolCardLimit           = 64
)

const (
	ModeHome Mode = iota
	ModeChat
	ModeSkills
	ModeEnvironments
	ModeModels
	ModeAuth
)

type ToolCard struct {
	ActionID  string
	TurnID    string
	Sequence  int64
	Name      string
	State     string
	Arguments string
	Output    string
	StartedAt time.Time
	Elapsed   time.Duration
}

// chatTimelineEntry is a TUI-owned projection over durable session and action
// facts. It deliberately does not own lifecycle state; its only job is to
// preserve the order in which those facts should be shown to a person.
type chatTimelineEntry struct {
	Sequence     int64
	Message      *session.Message
	Tool         *ToolCard
	Interruption *session.Interruption
	ControlID    string
}

type BragiCard struct {
	EntityID string
	Type     string
	State    string
	Revision int
	Entity   string
	Message  string
}

type slashOption struct {
	Command     string
	Description string
	NeedsValue  bool
}

var slashOptions = []slashOption{
	{Command: "/stop", Description: "Stop at the next safe boundary"},
	{Command: "/repo add", Description: "Add a repository to this chat", NeedsValue: true},
	{Command: "/env", Description: "Choose the project's runtime environment"},
	{Command: "/model", Description: "Choose a provider, model, and effort"},
	{Command: "/auth", Description: "Sign in to a model provider"},
	{Command: "/skills", Description: "Show skills available to this project"},
	{Command: "/quit", Description: "Leave Midgard"},
}

type Model struct {
	runtime              *local.Runtime
	ctx                  context.Context
	cancel               context.CancelFunc
	mode                 Mode
	width                int
	height               int
	viewport             viewport.Model
	composer             textarea.Model
	progress             spinner.Model
	summaries            []session.Summary
	selected             int
	sessionID            string
	messages             []session.Message
	interruptions        []session.Interruption
	turnUsages           []session.TurnUsage
	omittedMessages      int
	shortenedMessages    int
	omittedActivities    int
	controls             map[string]string
	controlContent       map[string]string
	controlSequences     map[string]int64
	tools                map[string]*ToolCard
	toolOrder            []string
	activity             chan agentloop.Activity
	done                 chan turnDone
	running              bool
	status               string
	gitStatus            string
	modelName            string
	providerCalls        int
	actions              int
	inputTokens          int64
	cacheHitInputTokens  int64
	cacheMissInputTokens int64
	outputTokens         int64
	thinkingTokens       int64
	providerDuration     time.Duration
	contextTokens        int64
	contextLimitTokens   int64
	contextEstimated     bool
	compactions          int
	skillStatuses        []local.SkillStatus
	skillSelected        int
	skillFilter          string
	maxCalls             int
	pendingRepoPath      string
	awaitingProjectName  bool
	repoFlow             bool
	pendingEnvironment   string
	environmentFlow      bool
	environmentOptions   []local.EnvironmentOption
	environmentSelected  int
	environmentFilter    string
	modelOptions         []local.ModelOption
	modelSelected        int
	modelFilter          string
	pendingModel         *local.ModelOption
	modelSwitching       bool
	queuedObjective      string
	authOptions          []local.AuthOption
	authSelected         int
	activeModelState     *BragiCard
	slashMenuOpen        bool
	slashSelected        int
	err                  error
}

type activityMsg agentloop.Activity
type turnDone struct {
	result agentloop.Result
	err    error
}
type sessionsMsg struct {
	items []session.Summary
	err   error
}
type loadedMsg struct {
	id                string
	objective         string
	messages          []session.Message
	interruptions     []session.Interruption
	turnUsages        []session.TurnUsage
	actions           []local.ActionTimeline
	omittedMessages   int
	shortenedMessages int
	omittedActivities int
	timelineLoaded    bool
	gitStatus         string
	preserveStatus    bool
	err               error
}
type steeredMsg struct {
	control session.Control
	err     error
}
type gitStatusMsg struct {
	status string
	err    error
}
type repoPreparedMsg struct {
	path string
	name string
	err  error
}
type repoAddedMsg struct {
	name string
	path string
	err  error
}
type environmentUsedMsg struct {
	option local.EnvironmentOption
	err    error
}
type environmentsMsg struct {
	options []local.EnvironmentOption
	silent  bool
	err     error
}
type skillsMsg struct {
	statuses []local.SkillStatus
	action   string
	name     string
	silent   bool
	err      error
}
type modelsMsg struct {
	options []local.ModelOption
	err     error
}
type modelUsedMsg struct {
	option local.ModelOption
	err    error
}
type authOptionsMsg struct {
	options []local.AuthOption
	err     error
}
type authFinishedMsg struct {
	provider string
	err      error
}

func New(ctx context.Context, runtime *local.Runtime, initialTask string) Model {
	composer := textarea.New()
	composer.Placeholder = "Describe a task or steer the active agent…"
	composer.ShowLineNumbers = false
	composer.SetHeight(1)
	composer.Focus()
	model := Model{
		runtime: runtime, ctx: ctx, mode: ModeHome, viewport: viewport.New(), composer: composer,
		progress: spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(colors.Warning)),
		controls: make(map[string]string), controlContent: make(map[string]string), controlSequences: make(map[string]int64), tools: make(map[string]*ToolCard),
		activity: make(chan agentloop.Activity, 256), done: make(chan turnDone, 1),
		modelName: runtime.Model, maxCalls: runtime.MaxProviderCalls,
	}
	if strings.TrimSpace(initialTask) != "" {
		model.mode = ModeChat
		model.status = "starting new session"
		model.composer.SetValue(initialTask)
	}
	return model
}

func (m Model) Init() tea.Cmd {
	if m.mode == ModeChat && strings.TrimSpace(m.composer.Value()) != "" {
		return tea.Batch(m.startNewCmd(m.composer.Value()), m.waitActivityCmd(), m.loadSkillCatalogCmd(), m.loadEnvironmentCatalogCmd(), m.modelsCmd())
	}
	return tea.Batch(m.loadSessionsCmd(), m.waitActivityCmd(), m.loadSkillCatalogCmd(), m.loadEnvironmentCatalogCmd(), m.modelsCmd())
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
	case sessionsMsg:
		m.summaries, m.err = msg.items, msg.err
		if len(m.summaries) > 0 {
			m.selected = 0
		}
	case loadedMsg:
		if msg.err != nil {
			m.err = msg.err
			if msg.preserveStatus {
				m.status = "could not refresh this chat"
			}
		} else {
			if msg.id != m.sessionID {
				m.tools = make(map[string]*ToolCard)
				m.toolOrder = nil
				m.omittedActivities = 0
				commands = append(commands, m.modelsCmd())
			}
			m.sessionID, m.messages, m.interruptions, m.turnUsages, m.gitStatus = msg.id, msg.messages, msg.interruptions, msg.turnUsages, msg.gitStatus
			m.omittedMessages, m.shortenedMessages = msg.omittedMessages, msg.shortenedMessages
			if msg.timelineLoaded {
				m.replaceToolTimeline(msg.actions)
				m.omittedActivities = msg.omittedActivities
			}
			m.controls = make(map[string]string)
			m.controlContent = make(map[string]string)
			m.controlSequences = make(map[string]int64)
			m.mode = ModeChat
			if !msg.preserveStatus {
				m.err = nil
			}
			if msg.preserveStatus && m.err == nil {
				m.activeModelState = nil
			}
			if !msg.preserveStatus && !m.repoFlow && !m.environmentFlow {
				m.status = "ready"
			}
			m.resize()
			if msg.preserveStatus {
				m.refreshViewport()
			} else {
				m.refreshViewportAtBottom()
			}
			if msg.objective != "" {
				m.composer.SetValue("")
				m.startTurn(msg.objective)
				commands = append(commands, m.waitDoneCmd(), m.progressTickCmd())
			}
		}
	case activityMsg:
		activity := agentloop.Activity(msg)
		m.applyActivity(activity)
		commands = append(commands, m.waitActivityCmd())
		if activity.Kind == "tool" && (activity.State == "succeeded" || activity.State == "failed" || activity.State == "invalid") {
			commands = append(commands, m.gitStatusCmd())
		}
	case turnDone:
		m.running, m.cancel = false, nil
		m.err = msg.err
		if msg.err != nil {
			m.status = "turn stopped: " + msg.err.Error()
		} else {
			m.status = "completed"
		}
		commands = append(commands, m.loadCurrentCmd())
		if m.pendingRepoPath != "" {
			path := m.pendingRepoPath
			m.pendingRepoPath = ""
			commands = append(commands, m.beginRepoAddCmd(path))
		}
		if m.pendingEnvironment != "" {
			name := m.pendingEnvironment
			m.pendingEnvironment = ""
			commands = append(commands, m.useEnvironmentCmd(name))
		}
		if m.pendingModel != nil {
			pending := *m.pendingModel
			m.pendingModel = nil
			m.modelSwitching = true
			m.status = "applying the queued model change"
			commands = append(commands, m.useModelCmd(pending))
		}
	case steeredMsg:
		m.err = msg.err
		if msg.err == nil {
			m.controls[msg.control.ControlID] = "queued"
			m.controlContent[msg.control.ControlID] = msg.control.Content
			m.controlSequences[msg.control.ControlID] = msg.control.Sequence
			m.status = "steering queued"
		}
	case gitStatusMsg:
		if msg.err == nil {
			m.gitStatus = msg.status
		}
	case spinner.TickMsg:
		var command tea.Cmd
		m.progress, command = m.progress.Update(msg)
		if m.running {
			commands = append(commands, command)
		}
	case repoPreparedMsg:
		m.err = msg.err
		if msg.err == nil {
			m.repoFlow = true
			m.pendingRepoPath, m.awaitingProjectName = msg.path, true
			m.status = fmt.Sprintf("Name this project to add %s", msg.name)
			m.composer.Placeholder = "Project name (for example, midgard-development)…"
		} else {
			m.status = "repository was not added"
		}
	case repoAddedMsg:
		m.err = msg.err
		m.awaitingProjectName, m.pendingRepoPath = false, ""
		m.composer.Placeholder = "Describe a task or steer the active agent…"
		if msg.err == nil {
			m.repoFlow = true
			m.status = fmt.Sprintf("%s is ready in this chat", msg.name)
			m.gitStatus = "clean"
		} else {
			m.status = "repository was not added"
		}
	case environmentsMsg:
		if msg.err != nil || !msg.silent {
			m.err = conversationalEnvironmentError(msg.err)
		}
		if msg.err == nil {
			m.environmentOptions = msg.options
			if m.environmentSelected >= len(m.filteredEnvironmentOptions()) {
				m.environmentSelected = max(0, len(m.filteredEnvironmentOptions())-1)
			}
		}
	case environmentUsedMsg:
		m.err = conversationalEnvironmentError(msg.err)
		if msg.err == nil {
			m.environmentFlow = true
			m.runtime.EnvironmentName = msg.option.Name
			for index := range m.environmentOptions {
				m.environmentOptions[index].Active = m.environmentOptions[index].Name == msg.option.Name
			}
			m.mode = ModeChat
			m.environmentFilter = ""
			m.status = fmt.Sprintf("later turns will use environment %s", msg.option.Name)
			m.refreshViewport()
		}
	case skillsMsg:
		if msg.err != nil || !msg.silent {
			m.err = msg.err
		}
		if msg.err == nil {
			m.skillStatuses = msg.statuses
			if m.skillSelected >= len(m.filteredSkillStatuses()) {
				m.skillSelected = max(0, len(m.filteredSkillStatuses())-1)
			}
			if msg.silent {
				break
			}
			switch msg.action {
			case "enable":
				m.status = fmt.Sprintf("%s is available to later turns", msg.name)
			case "disable":
				m.status = fmt.Sprintf("%s is hidden from later turns", msg.name)
			default:
				m.status = "project skill availability"
			}
		}
	case modelsMsg:
		m.err = msg.err
		if msg.err == nil {
			m.modelOptions = msg.options
			m.modelSelected = m.selectedModelIndex()
		}
	case modelUsedMsg:
		m.modelSwitching = false
		m.err = msg.err
		if msg.err == nil {
			m.modelName = msg.option.Model
			for index := range m.modelOptions {
				m.modelOptions[index].Selected = m.modelOptions[index].Provider == msg.option.Provider && m.modelOptions[index].Model == msg.option.Model
				if m.modelOptions[index].Selected {
					m.modelOptions[index].Effort = msg.option.Effort
				}
			}
			m.mode, m.modelFilter = ModeChat, ""
			m.status = fmt.Sprintf("later model calls will use %s · %s effort", msg.option.Name, msg.option.Effort)
			if m.queuedObjective != "" {
				objective := m.queuedObjective
				m.queuedObjective = ""
				m.startTurn(objective)
				commands = append(commands, m.waitDoneCmd(), m.progressTickCmd())
			}
		} else if m.queuedObjective != "" {
			m.composer.SetValue(m.queuedObjective)
			m.queuedObjective = ""
			m.status = "model change needs attention before the next task can start"
		}
	case authOptionsMsg:
		m.err = msg.err
		if msg.err == nil {
			m.authOptions = msg.options
		}
	case authFinishedMsg:
		m.err = msg.err
		if msg.err == nil {
			m.status = fmt.Sprintf("%s authentication updated", msg.provider)
		}
		commands = append(commands, m.authOptionsCmd())
	case tea.KeyPressMsg:
		key := msg.String()
		switch m.mode {
		case ModeHome:
			switch key {
			case "up", "k":
				if m.selected > 0 {
					m.selected--
				}
			case "down", "j":
				if m.selected+1 < len(m.summaries) {
					m.selected++
				}
			case "enter":
				if len(m.summaries) > 0 {
					return m, m.openSessionCmd(m.summaries[m.selected].SessionID)
				}
			case "n":
				m.mode, m.status = ModeChat, "new session"
				m.composer.SetValue("")
			case "q", "ctrl+c":
				return m, tea.Quit
			}
		case ModeChat:
			switch key {
			case "alt+left":
				return m.stepEffort(-1)
			case "alt+right":
				return m.stepEffort(1)
			case "up":
				if m.slashMenuOpen {
					options := m.filteredSlashOptions()
					m.slashSelected = (m.slashSelected - 1 + len(options)) % len(options)
					return m, nil
				}
			case "down":
				if m.slashMenuOpen {
					options := m.filteredSlashOptions()
					m.slashSelected = (m.slashSelected + 1) % len(options)
					return m, nil
				}
			case "tab":
				if m.slashMenuOpen {
					m.completeSlashSelection()
					return m, nil
				}
			case "ctrl+c":
				m.composer.SetValue("")
				m.closeSlashMenu()
				return m, nil
			case "esc":
				if m.slashMenuOpen {
					m.composer.SetValue("")
					m.closeSlashMenu()
					return m, nil
				}
				if !m.running {
					m.mode = ModeHome
					return m, m.loadSessionsCmd()
				}
			case "enter":
				value := strings.TrimSpace(m.composer.Value())
				if m.slashMenuOpen {
					options := m.filteredSlashOptions()
					if len(options) > 0 {
						selected := options[m.slashSelected]
						if selected.NeedsValue {
							m.composer.SetValue(selected.Command + " ")
							m.closeSlashMenu()
							return m, nil
						}
						value = selected.Command
					}
				}
				if value != "" {
					m.composer.SetValue("")
					m.closeSlashMenu()
					if m.awaitingProjectName {
						if value == "/cancel" {
							m.awaitingProjectName, m.pendingRepoPath = false, ""
							m.repoFlow = false
							m.composer.Placeholder = "Describe a task or steer the active agent…"
							m.status = "repository addition cancelled"
							return m, nil
						}
						path := m.pendingRepoPath
						m.status = "preparing the project and repository"
						return m, m.addRepoCmd(path, value)
					}
					switch value {
					case "/stop":
						if m.running && m.cancel != nil {
							m.cancel()
							m.status = "stopping at the current boundary"
						} else {
							m.status = "no turn is currently running"
						}
						m.refreshViewport()
						return m, nil
					case "/quit":
						return m, tea.Quit
					case "/model":
						m.mode, m.modelFilter = ModeModels, ""
						return m, m.modelsCmd()
					case "/auth":
						m.mode = ModeAuth
						return m, m.authOptionsCmd()
					}
					if path, ok, commandErr := parseRepoAdd(value); ok {
						if commandErr != nil {
							m.err = commandErr
							m.status = "repository was not added"
							return m, nil
						}
						if m.sessionID == "" {
							m.err = errors.New("start a chat first, then use `/repo add PATH` so Midgard knows which chat should receive the repository")
							return m, nil
						}
						m.err = nil
						if m.running {
							m.repoFlow = true
							m.pendingRepoPath = path
							m.status = "repository will be added after the current turn finishes"
							return m, nil
						}
						return m, m.beginRepoAddCmd(path)
					}
					if _, _, ok, commandErr := parseEnvironmentCommand(value); ok {
						if commandErr != nil {
							m.err, m.status = commandErr, "environment was not changed"
							return m, nil
						}
						m.mode = ModeEnvironments
						return m, m.environmentsCmd(false)
					}
					if skillCommand, _, ok, commandErr := parseSkillCommand(value); ok {
						if commandErr != nil {
							m.err, m.status = commandErr, "skill availability was not changed"
							return m, nil
						}
						if skillCommand == "status" {
							m.mode = ModeSkills
							return m, m.skillsStatusCmd()
						}
					}
					if m.running {
						m.repoFlow = false
						m.environmentFlow = false
						return m, m.steerCmd(value)
					}
					if m.modelSwitching {
						m.queuedObjective = value
						m.status = "task saved · starting after the queued model change"
						return m, nil
					}
					if m.sessionID == "" {
						m.repoFlow = false
						m.environmentFlow = false
						return m, m.startNewCmd(value)
					}
					m.repoFlow = false
					m.environmentFlow = false
					m.startTurn(value)
					return m, tea.Batch(m.waitDoneCmd(), m.progressTickCmd())
				}
			}
		case ModeSkills:
			switch key {
			case "up":
				if m.skillSelected > 0 {
					m.skillSelected--
				}
			case "down":
				if m.skillSelected+1 < len(m.filteredSkillStatuses()) {
					m.skillSelected++
				}
			case "space", " ":
				visible := m.filteredSkillStatuses()
				if len(visible) == 0 {
					return m, nil
				}
				if m.running {
					m.err = errors.New("wait for the current turn to finish before changing its skill catalog")
					return m, nil
				}
				selected := visible[m.skillSelected]
				return m, m.setSkillEnabledCmd(selected.Name, !selected.Enabled)
			case "backspace":
				runes := []rune(m.skillFilter)
				if len(runes) > 0 {
					m.skillFilter = string(runes[:len(runes)-1])
					m.skillSelected = 0
				}
			case "esc":
				if m.skillFilter != "" {
					m.skillFilter = ""
					m.skillSelected = 0
					return m, nil
				}
				m.mode = ModeChat
				m.err = nil
				m.refreshViewport()
			case "ctrl+c":
				m.mode = ModeChat
				m.skillFilter = ""
				m.err = nil
				m.refreshViewport()
			default:
				if msg.Text != "" && msg.Text != " " && msg.Mod == 0 {
					m.skillFilter += msg.Text
					m.skillSelected = 0
				}
			}
		case ModeEnvironments:
			switch key {
			case "up":
				if m.environmentSelected > 0 {
					m.environmentSelected--
				}
			case "down":
				if m.environmentSelected+1 < len(m.filteredEnvironmentOptions()) {
					m.environmentSelected++
				}
			case "enter":
				visible := m.filteredEnvironmentOptions()
				if len(visible) == 0 {
					return m, nil
				}
				selected := visible[m.environmentSelected]
				if m.running {
					m.environmentFlow = true
					m.pendingEnvironment = selected.Name
					m.mode = ModeChat
					m.environmentFilter = ""
					m.status = fmt.Sprintf("%s will be used after the current turn finishes", selected.Name)
					return m, nil
				}
				return m, m.useEnvironmentCmd(selected.Name)
			case "backspace":
				runes := []rune(m.environmentFilter)
				if len(runes) > 0 {
					m.environmentFilter = string(runes[:len(runes)-1])
					m.environmentSelected = 0
				}
			case "esc":
				if m.environmentFilter != "" {
					m.environmentFilter = ""
					m.environmentSelected = 0
					return m, nil
				}
				m.mode = ModeChat
				m.err = nil
				m.refreshViewport()
			case "ctrl+c":
				m.mode = ModeChat
				m.environmentFilter = ""
				m.err = nil
				m.refreshViewport()
			default:
				if msg.Text != "" && msg.Text != " " && msg.Mod == 0 {
					m.environmentFilter += msg.Text
					m.environmentSelected = 0
				}
			}
		case ModeModels:
			switch key {
			case "up":
				if m.modelSelected > 0 {
					m.modelSelected--
				}
			case "down":
				if m.modelSelected+1 < len(m.filteredModelOptions()) {
					m.modelSelected++
				}
			case "left", "alt+left":
				m.adjustPickerEffort(-1)
			case "right", "alt+right":
				m.adjustPickerEffort(1)
			case "enter":
				visible := m.filteredModelOptions()
				if len(visible) == 0 || visible[m.modelSelected].Model == "" {
					return m, nil
				}
				return m.chooseModel(visible[m.modelSelected])
			case "backspace":
				runes := []rune(m.modelFilter)
				if len(runes) > 0 {
					m.modelFilter = string(runes[:len(runes)-1])
					m.modelSelected = 0
				}
			case "esc", "ctrl+c":
				if key == "esc" && m.modelFilter != "" {
					m.modelFilter = ""
					m.modelSelected = 0
					return m, nil
				}
				m.mode, m.modelFilter, m.err = ModeChat, "", nil
				m.refreshViewport()
			default:
				if msg.Text != "" && msg.Mod == 0 {
					m.modelFilter += msg.Text
					m.modelSelected = 0
				}
			}
		case ModeAuth:
			switch key {
			case "up":
				if m.authSelected > 0 {
					m.authSelected--
				}
			case "down":
				if m.authSelected+1 < len(m.authOptions) {
					m.authSelected++
				}
			case "enter":
				if len(m.authOptions) > 0 {
					return m, m.loginCmd(m.authOptions[m.authSelected])
				}
			case "esc", "ctrl+c":
				m.mode, m.err = ModeChat, nil
				m.refreshViewport()
			}
		}
	}
	if m.mode == ModeChat {
		var cmd tea.Cmd
		m.composer, cmd = m.composer.Update(message)
		commands = append(commands, cmd)
		m.updateSlashMenu()
		m.viewport, cmd = m.viewport.Update(message)
		commands = append(commands, cmd)
	}
	return m, tea.Batch(commands...)
}

func (m Model) View() tea.View {
	var body string
	switch m.mode {
	case ModeHome:
		body = m.homeView()
	case ModeChat:
		body = m.chatView()
	case ModeSkills:
		body = m.skillsView()
	case ModeEnvironments:
		body = m.environmentsView()
	case ModeModels:
		body = m.modelsView()
	case ModeAuth:
		body = m.authView()
	}
	view := tea.NewView(body)
	view.AltScreen = true
	return view
}

func (m *Model) startTurn(objective string) {
	if m.running || m.sessionID == "" {
		return
	}
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.cancel, m.running, m.status = cancel, true, "agent working"
	m.providerCalls, m.actions = 0, 0
	m.inputTokens, m.cacheHitInputTokens, m.cacheMissInputTokens, m.outputTokens = 0, 0, 0, 0
	m.thinkingTokens, m.providerDuration = 0, 0
	m.activeModelState = nil
	presentedObjective := objective
	if len(presentedObjective) > tuiMessageCharacters {
		presentedObjective = bounded(presentedObjective, tuiMessageCharacters) + "\n\n[Message shortened in the TUI; complete content remains in session history.]"
		m.shortenedMessages++
	}
	m.messages = append(m.messages, session.Message{Role: "user", Content: presentedObjective})
	m.resize()
	m.refreshViewportAtBottom()
	sink := agentloop.ActivityFunc(func(activity agentloop.Activity) {
		select {
		case m.activity <- activity:
		case <-turnCtx.Done():
		}
	})
	go func() {
		result, err := m.runtime.RunTurn(turnCtx, m.sessionID, objective, sink)
		m.done <- turnDone{result: result, err: err}
	}()
}

func (m Model) startNewCmd(objective string) tea.Cmd {
	return func() tea.Msg {
		id, err := m.runtime.NewSession(m.ctx, objective)
		if err != nil {
			return loadedMsg{err: err}
		}
		return loadedMsg{id: id, objective: objective, messages: nil, gitStatus: "clean"}
	}
}

func (m Model) loadSessionsCmd() tea.Cmd {
	return func() tea.Msg {
		items, err := m.runtime.SessionSummaries(m.ctx)
		return sessionsMsg{items: items, err: err}
	}
}

func (m Model) openSessionCmd(id string) tea.Cmd {
	return func() tea.Msg {
		if err := m.runtime.Recover(m.ctx, id); err != nil {
			return loadedMsg{err: err}
		}
		return m.loadTranscript(id, false)
	}
}

func (m Model) loadCurrentCmd() tea.Cmd {
	if m.sessionID == "" {
		return nil
	}
	return func() tea.Msg {
		return m.loadTranscript(m.sessionID, true)
	}
}

func (m Model) loadTranscript(id string, preserveStatus bool) loadedMsg {
	window, err := m.runtime.RecentMessages(m.ctx, id, tuiTranscriptBytes, tuiMessageCharacters)
	minSequence := int64(0)
	if len(window.Messages) > 0 {
		minSequence = window.Messages[0].Sequence
	}
	interruptions, interruptionErr := m.runtime.RecentInterruptions(m.ctx, id, minSequence, tuiInterruptionLimit)
	turnUsages, usageErr := m.runtime.RecentTurnUsages(m.ctx, id, tuiTurnUsageLimit)
	actions, actionErr := m.runtime.RecentActions(m.ctx, id, minSequence, tuiToolCardLimit)
	gitStatus, statusErr := m.runtime.GitStatus(m.ctx, id)
	if err == nil {
		err = interruptionErr
	}
	if err == nil {
		err = usageErr
	}
	if err == nil {
		err = actionErr
	}
	if err == nil {
		err = statusErr
	}
	return loadedMsg{id: id, messages: window.Messages, interruptions: interruptions, turnUsages: turnUsages, actions: actions.Items,
		omittedMessages: window.OmittedMessages, shortenedMessages: window.ShortenedMessages,
		omittedActivities: actions.Omitted, timelineLoaded: true, gitStatus: gitStatus, preserveStatus: preserveStatus, err: err}
}

func (m Model) steerCmd(content string) tea.Cmd {
	return func() tea.Msg {
		control, err := m.runtime.Steer(m.ctx, m.sessionID, content)
		return steeredMsg{control: control, err: err}
	}
}

func (m Model) gitStatusCmd() tea.Cmd {
	return func() tea.Msg {
		status, err := m.runtime.GitStatus(m.ctx, m.sessionID)
		return gitStatusMsg{status: status, err: err}
	}
}

func (m Model) beginRepoAddCmd(path string) tea.Cmd {
	if m.runtime.Project.Implicit {
		return func() tea.Msg {
			mount, err := m.runtime.PrepareRepository(m.ctx, path)
			return repoPreparedMsg{path: path, name: mount.Name, err: conversationalRepoError(err)}
		}
	}
	return m.addRepoCmd(path, "")
}

func (m Model) addRepoCmd(path, projectName string) tea.Cmd {
	return func() tea.Msg {
		mount, err := m.runtime.AddRepository(m.ctx, m.sessionID, path, projectName)
		return repoAddedMsg{name: mount.Name, path: mount.Path, err: conversationalRepoError(err)}
	}
}

func (m Model) environmentsCmd(silent bool) tea.Cmd {
	return func() tea.Msg {
		options, err := m.runtime.Environments()
		return environmentsMsg{options: options, silent: silent, err: err}
	}
}

func (m Model) loadEnvironmentCatalogCmd() tea.Cmd { return m.environmentsCmd(true) }

func (m Model) useEnvironmentCmd(name string) tea.Cmd {
	return func() tea.Msg {
		snapshot, err := m.runtime.UseEnvironment(m.ctx, m.sessionID, name)
		return environmentUsedMsg{option: local.EnvironmentOption{Name: snapshot.Name, Active: true, Variables: snapshot.Inspect()}, err: err}
	}
}

func (m Model) skillsStatusCmd() tea.Cmd {
	return func() tea.Msg {
		statuses, err := m.runtime.SkillStatuses()
		return skillsMsg{statuses: statuses, err: err}
	}
}

func (m Model) loadSkillCatalogCmd() tea.Cmd {
	return func() tea.Msg {
		statuses, err := m.runtime.SkillStatuses()
		return skillsMsg{statuses: statuses, silent: true, err: err}
	}
}

func (m Model) setSkillEnabledCmd(name string, enabled bool) tea.Cmd {
	return func() tea.Msg {
		statuses, err := m.runtime.SetSkillEnabled(name, enabled)
		action := "disable"
		if enabled {
			action = "enable"
		}
		return skillsMsg{statuses: statuses, action: action, name: name, err: err}
	}
}

func (m Model) modelsCmd() tea.Cmd {
	return func() tea.Msg {
		options, err := m.runtime.ModelOptions(m.ctx)
		return modelsMsg{options: options, err: err}
	}
}

func (m Model) useModelCmd(option local.ModelOption) tea.Cmd {
	return func() tea.Msg {
		selected, err := m.runtime.SelectModel(m.ctx, m.sessionID, option.Provider, option.Model, option.Effort)
		return modelUsedMsg{option: selected, err: err}
	}
}

func (m Model) authOptionsCmd() tea.Cmd {
	return func() tea.Msg {
		options, err := m.runtime.AuthOptions(m.ctx)
		return authOptionsMsg{options: options, err: err}
	}
}

func (m Model) loginCmd(option local.AuthOption) tea.Cmd {
	var command *exec.Cmd
	if option.Provider == "codex" {
		command = exec.Command("codex", "login")
	} else {
		executable, err := os.Executable()
		if err != nil {
			return func() tea.Msg { return authFinishedMsg{provider: option.Name, err: err} }
		}
		command = exec.Command(executable, "auth", "login", option.Provider, "--profile", option.Profile, "--credential", "api-key")
	}
	return tea.ExecProcess(command, func(err error) tea.Msg { return authFinishedMsg{provider: option.Name, err: err} })
}

func (m Model) chooseModel(option local.ModelOption) (tea.Model, tea.Cmd) {
	if m.running {
		copy := option
		m.pendingModel = &copy
		m.mode, m.modelFilter = ModeChat, ""
		m.status = fmt.Sprintf("%s · %s effort will be used after the current turn", option.Name, option.Effort)
		return m, nil
	}
	return m, m.useModelCmd(option)
}

func (m Model) stepEffort(direction int) (tea.Model, tea.Cmd) {
	index := m.selectedModelIndex()
	if index < 0 || index >= len(m.modelOptions) {
		m.status = "open /model to choose a model first"
		return m, nil
	}
	option := m.modelOptions[index]
	current := slices.Index(option.Efforts, option.Effort)
	if current < 0 {
		current = 0
	}
	next := max(0, min(len(option.Efforts)-1, current+direction))
	if len(option.Efforts) == 0 || next == current {
		return m, nil
	}
	option.Effort = option.Efforts[next]
	return m.chooseModel(option)
}

func (m *Model) adjustPickerEffort(direction int) {
	visible := m.filteredModelOptions()
	if len(visible) == 0 || visible[m.modelSelected].Model == "" {
		return
	}
	selected := visible[m.modelSelected]
	current := slices.Index(selected.Efforts, selected.Effort)
	if current < 0 {
		current = 0
	}
	next := max(0, min(len(selected.Efforts)-1, current+direction))
	if len(selected.Efforts) == 0 {
		return
	}
	for index := range m.modelOptions {
		if m.modelOptions[index].Provider == selected.Provider && m.modelOptions[index].Model == selected.Model {
			m.modelOptions[index].Effort = selected.Efforts[next]
		}
	}
}

func (m Model) selectedModelIndex() int {
	for index, option := range m.modelOptions {
		if option.Selected {
			return index
		}
	}
	return 0
}

func parseRepoAdd(value string) (string, bool, error) {
	if value != "/repo" && !strings.HasPrefix(value, "/repo ") {
		return "", false, nil
	}
	if value != "/repo add" && !strings.HasPrefix(value, "/repo add ") {
		return "", true, errors.New("Midgard expected `/repo add PATH`; include the Git repository path you want available in this chat")
	}
	path := strings.TrimSpace(strings.TrimPrefix(value, "/repo add"))
	if path == "" {
		return "", true, errors.New("Midgard expected `/repo add PATH`; include the Git repository path you want available in this chat")
	}
	return path, true, nil
}

func conversationalRepoError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("Midgard could not add that repository, so this chat still has its previous repositories. %v", err)
}

func parseEnvironmentCommand(value string) (command, name string, recognized bool, err error) {
	if value != "/env" && !strings.HasPrefix(value, "/env ") {
		return "", "", false, nil
	}
	if strings.TrimSpace(value) == "/env" {
		return "open", "", true, nil
	}
	return "", "", true, errors.New("Midgard expected `/env`; choose an environment with Enter in the picker")
}

func parseSkillCommand(value string) (command, name string, recognized bool, err error) {
	if value != "/skills" && !strings.HasPrefix(value, "/skills ") {
		return "", "", false, nil
	}
	fields := strings.Fields(value)
	if len(fields) == 1 {
		return "status", "", true, nil
	}
	return "", "", true, errors.New("Midgard expected `/skills`; use Space in the skill picker to change availability")
}

func conversationalEnvironmentError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("Midgard could not use that runtime environment, so command injection remains unchanged. %v", err)
}

func (m Model) waitActivityCmd() tea.Cmd { return func() tea.Msg { return activityMsg(<-m.activity) } }
func (m Model) waitDoneCmd() tea.Cmd     { return func() tea.Msg { return <-m.done } }
func (m Model) progressTickCmd() tea.Cmd { return func() tea.Msg { return m.progress.Tick() } }

func (m *Model) applyActivity(activity agentloop.Activity) {
	if activity.ProviderCalls > m.providerCalls {
		m.providerCalls = activity.ProviderCalls
	}
	if activity.Actions > m.actions {
		m.actions = activity.Actions
	}
	if activity.Kind == "provider" {
		m.inputTokens = activity.InputTokens
		m.cacheHitInputTokens = activity.CacheHitInputTokens
		m.cacheMissInputTokens = activity.CacheMissInputTokens
		m.outputTokens = activity.OutputTokens
		m.thinkingTokens = activity.ThinkingTokens
		m.providerDuration = activity.ProviderDuration
	}
	if activity.Kind == "provider" || activity.Kind == "context" {
		m.contextTokens = activity.ContextTokens
		m.contextLimitTokens = activity.ContextLimitTokens
		m.contextEstimated = activity.ContextEstimated
		m.compactions = activity.Compactions
	}
	if activity.Kind == "tool" {
		if activity.State == "queued" {
			m.activeModelState = nil
		}
		card := m.tools[activity.ActionID]
		if card == nil {
			card = &ToolCard{ActionID: activity.ActionID, TurnID: activity.TurnID, Sequence: activity.Sequence, Name: activity.Name, StartedAt: activity.At}
			m.tools[activity.ActionID] = card
			m.toolOrder = append(m.toolOrder, activity.ActionID)
			m.pruneToolCards()
		}
		previous := card.State
		if activity.Sequence > 0 && card.Sequence == 0 {
			card.Sequence = activity.Sequence
		}
		card.State = activity.State
		if activity.Arguments != "" {
			card.Arguments = activity.Arguments
		}
		if activity.Output != "" {
			card.Output = activity.Output
		}
		card.Elapsed = activity.At.Sub(card.StartedAt)
		if (activity.State == "succeeded" || activity.State == "failed" || activity.State == "invalid") && previous != "succeeded" && previous != "failed" && previous != "invalid" {
			m.actions++
		}
	}
	if activity.Kind == "control" && activity.ControlID != "" {
		m.controls[activity.ControlID] = activity.State
		m.controlContent[activity.ControlID] = activity.Message
		if activity.Sequence > 0 {
			m.controlSequences[activity.ControlID] = activity.Sequence
		}
	}
	if activity.Kind == "message" && activity.Sequence > 0 && (activity.Role == "user" || activity.Role == "assistant") {
		m.attachMessageSequence(activity)
	}
	if activity.Kind == "model_state" && activity.EntityID != "" {
		card := m.activeModelState
		if card == nil || card.EntityID != activity.EntityID {
			card = &BragiCard{EntityID: activity.EntityID}
			m.activeModelState = card
		}
		card.Type, card.State, card.Revision = activity.Name, activity.State, activity.Revision
		if activity.Arguments != "" {
			card.Entity = activity.Arguments
		}
		if activity.Message != "" {
			card.Message = activity.Message
		}
		if card.Type == "message" {
			m.activeModelState = nil
			m.status = "responding"
		}
	}
	switch activity.Kind {
	case "turn":
		if activity.State == "started" {
			m.status = "preparing this turn"
		}
	case "provider":
		if activity.State == "running" {
			m.activeModelState = nil
			m.status = m.providerStatus("waiting for the model")
		} else if activity.State == "completed" {
			m.status = m.providerStatus("thinking")
		}
	case "tool":
		switch activity.State {
		case "queued":
			m.status = fmt.Sprintf("preparing %s", activity.Name)
		case "validated":
			m.status = fmt.Sprintf("%s intent validated", activity.Name)
		case "committed":
			m.status = fmt.Sprintf("%s committed · dispatch is now allowed", activity.Name)
		case "running":
			m.status = fmt.Sprintf("running %s", activity.Name)
		case "succeeded":
			m.status = fmt.Sprintf("%s finished", activity.Name)
		case "failed":
			m.status = fmt.Sprintf("%s failed · the agent can recover", activity.Name)
		case "invalid":
			m.status = fmt.Sprintf("%s was not run · the agent is correcting its request", activity.Name)
		}
	case "context":
		if activity.State == "compacted" {
			m.status = "context compacted · continuing with recent evidence"
		}
	case "control":
		if activity.State == "queued" {
			m.status = "steering saved · waiting for a safe boundary"
		} else if activity.State == "acknowledged" {
			m.status = "steering is now in model context"
		}
	case "agent":
		if activity.Message != "" {
			m.status = "model response received"
		}
	case "final":
		m.status = "checking completion evidence"
	case "model_state":
		if activity.Name == "message" {
			m.status = "responding"
			break
		}
		switch activity.State {
		case "op.accepted":
			m.status = fmt.Sprintf("materializing %s draft", valueOr(activity.Name, "model"))
		case "commit.proposed":
			m.status = fmt.Sprintf("validating model commit %s", activity.EntityID)
		case "commit.accepted":
			if activity.Name == "completion" {
				m.activeModelState = nil
				m.status = "checking the proposed response"
			} else {
				m.status = fmt.Sprintf("model commit %s accepted", activity.EntityID)
			}
		case "source.rejected", "op.rejected", "commit.rejected":
			m.status = fmt.Sprintf("model record %s rejected · waiting for repair", valueOr(activity.EntityID, ""))
		}
	case "stream":
		switch activity.State {
		case string(provider.LiveThinking):
			m.activeModelState = nil
			m.status = "thinking"
		case string(provider.LiveOutput):
			m.status = "receiving model updates"
		}
	}
	m.refreshViewport()
}

func (m *Model) attachMessageSequence(activity agentloop.Activity) {
	for index := len(m.messages) - 1; index >= 0; index-- {
		message := &m.messages[index]
		if message.Sequence == activity.Sequence {
			return
		}
		if message.Sequence == 0 && message.Role == activity.Role && message.Content == activity.Message && (message.TurnID == "" || message.TurnID == activity.TurnID) {
			message.TurnID, message.Sequence = activity.TurnID, activity.Sequence
			return
		}
	}
	m.messages = append(m.messages, session.Message{SessionID: activity.SessionID, TurnID: activity.TurnID, Role: activity.Role, Content: activity.Message, Sequence: activity.Sequence})
}

func (m *Model) replaceToolTimeline(actions []local.ActionTimeline) {
	m.tools = make(map[string]*ToolCard, len(actions))
	m.toolOrder = make([]string, 0, len(actions))
	for _, item := range actions {
		elapsed := time.Duration(0)
		if !item.FinishedAt.IsZero() && item.FinishedAt.After(item.StartedAt) {
			elapsed = item.FinishedAt.Sub(item.StartedAt)
		}
		m.tools[item.ActionID] = &ToolCard{
			ActionID: item.ActionID, TurnID: item.TurnID, Sequence: item.StartedSequence,
			Name: presentationToolName(item.Capability), State: presentationActionState(string(item.State)),
			Arguments: string(item.Arguments), Output: string(item.Result), StartedAt: item.StartedAt, Elapsed: elapsed,
		}
		m.toolOrder = append(m.toolOrder, item.ActionID)
	}
}

func presentationToolName(capability string) string {
	return strings.ReplaceAll(capability, ".", "_")
}

func presentationActionState(state string) string {
	switch state {
	case "intent":
		return "queued"
	case "validated":
		return "validated"
	case "approval_pending":
		return "waiting"
	case "committed":
		return "committed"
	case "dispatched":
		return "running"
	case "succeeded", "failed":
		return state
	case "retracted", "rejected":
		return "invalid"
	case "compensation_committed":
		return "failed"
	default:
		return "queued"
	}
}

func (m *Model) resize() {
	follow := m.viewport.AtBottom()
	m.viewport.SetWidth(max(20, m.width-2))
	menuHeight := 0
	if m.slashMenuOpen {
		menuHeight = len(m.filteredSlashOptions())
	}
	usageHeight := 0
	if _, ok := m.latestCompletedUsage(); ok {
		usageHeight = 1
	}
	m.viewport.SetHeight(max(5, m.height-8-menuHeight-usageHeight))
	m.composer.SetWidth(max(20, m.width-4))
	m.refreshViewportWithFollow(follow)
}

func (m *Model) refreshViewport() {
	m.refreshViewportWithFollow(m.viewport.AtBottom())
}

func (m *Model) refreshViewportAtBottom() {
	m.refreshViewportWithFollow(true)
}

func (m *Model) refreshViewportWithFollow(follow bool) {
	offset := m.viewport.YOffset()
	m.viewport.SetContent(limitRenderedChatLines(m.chatContent(), tuiTranscriptRenderedLines))
	if follow {
		m.viewport.GotoBottom()
	} else {
		m.viewport.SetYOffset(offset)
	}
}

// limitRenderedChatLines is the final presentation guard after Markdown
// styling, diff previews, and width-dependent wrapping. It retains the newest
// lines so the active task stays visible; the complete durable session remains
// stored outside the active terminal viewport.
func limitRenderedChatLines(content string, limit int) string {
	if limit <= 0 {
		return ""
	}
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= limit {
		return content
	}
	hidden := len(lines) - (limit - 1)
	marker := colors.Muted.Render(fmt.Sprintf("… %d older rendered lines are hidden; complete history remains saved", hidden))
	if limit == 1 {
		return marker
	}
	return marker + "\n" + strings.Join(lines[len(lines)-(limit-1):], "\n")
}

func (m Model) homeView() string {
	var output strings.Builder
	fmt.Fprintf(&output, "%s  %s\n", colors.Brand.Render("MIDGARD"), colors.Muted.Render("repository sessions"))
	fmt.Fprintf(&output, "%s  %s\n", colors.Muted.Render("repository"), colors.Location.Render(m.runtime.Repository))
	fmt.Fprintf(&output, "%s\n%s\n", divider(m.width), colors.Section.Render("CHATS"))
	if len(m.summaries) == 0 {
		fmt.Fprintf(&output, "\n  %s\n", colors.Warning.Render("No chats yet. Press n to describe the first task."))
	}
	for index, item := range m.summaries {
		cursor := colors.Muted.Render("  ")
		objective := item.Objective
		if index == m.selected {
			cursor = colors.Accent.Render("› ")
			objective = colors.Selected.Render(objective)
		}
		fmt.Fprintf(&output, "%s%s  %s\n", cursor, styledStatus(item.Status), objective)
	}
	fmt.Fprintf(&output, "\n%s", colors.Muted.Render("↑/↓ select   enter open   n new chat   q quit"))
	if m.err != nil {
		fmt.Fprintf(&output, "\n\n%s", colors.Failure.Render("Midgard needs attention: "+m.err.Error()))
	}
	return output.String()
}

func (m Model) chatView() string {
	chatID := shortID(m.sessionID)
	if chatID == "" {
		chatID = "new chat"
	}
	header := fmt.Sprintf("%s  %s\n", colors.Brand.Render("MIDGARD"), colors.Accent.Render(chatID))
	header += fmt.Sprintf("%s  %s\n", colors.Muted.Render("repository"), colors.Location.Render(m.runtime.Repository))
	header += divider(m.width) + "\n"
	footer := "\n" + divider(m.width) + "\n"
	if menu := m.slashMenuView(); menu != "" {
		footer += menu + "\n"
	}
	footer += m.composer.View()
	if current, ok := m.currentModelOption(); ok {
		footer += "\n" + colors.Muted.Render(fmt.Sprintf("%s · %s effort   alt+←/→ effort   /model switch", current.Name, current.Effort))
	}
	if usage, ok := m.latestCompletedUsage(); ok {
		footer += "\n" + renderStyledTurnCost(usage)
	}
	if m.err != nil {
		footer += "\n" + colors.Failure.Render("Midgard needs attention: "+m.err.Error())
	}
	return header + m.viewport.View() + footer
}

func (m Model) currentModelOption() (local.ModelOption, bool) {
	if m.pendingModel != nil {
		return *m.pendingModel, true
	}
	for _, option := range m.modelOptions {
		if option.Selected {
			return option, true
		}
	}
	return local.ModelOption{}, false
}

func (m Model) latestCompletedUsage() (session.TurnUsage, bool) {
	if m.running || len(m.messages) == 0 {
		return session.TurnUsage{}, false
	}
	last := m.messages[len(m.messages)-1]
	if last.Role != "assistant" {
		return session.TurnUsage{}, false
	}
	for index := len(m.turnUsages) - 1; index >= 0; index-- {
		if m.turnUsages[index].TurnID == last.TurnID {
			return m.turnUsages[index], true
		}
	}
	return session.TurnUsage{}, false
}

func (m *Model) updateSlashMenu() {
	options := m.filteredSlashOptions()
	m.slashMenuOpen = strings.HasPrefix(m.composer.Value(), "/") && len(options) > 0
	if !m.slashMenuOpen {
		m.slashSelected = 0
	} else if m.slashSelected >= len(options) {
		m.slashSelected = len(options) - 1
	}
	if m.width > 0 && m.height > 0 {
		m.resize()
	}
}

func (m *Model) closeSlashMenu() {
	m.slashMenuOpen = false
	m.slashSelected = 0
	if m.width > 0 && m.height > 0 {
		m.resize()
	}
}

func (m Model) filteredSlashOptions() []slashOption {
	query := m.composer.Value()
	if !strings.HasPrefix(query, "/") {
		return nil
	}
	options := make([]slashOption, 0, len(slashOptions))
	for _, option := range slashOptions {
		if strings.HasPrefix(option.Command, query) {
			options = append(options, option)
		}
	}
	return options
}

func (m *Model) completeSlashSelection() {
	options := m.filteredSlashOptions()
	if len(options) == 0 {
		return
	}
	selected := options[m.slashSelected]
	suffix := ""
	if selected.NeedsValue {
		suffix = " "
	}
	m.composer.SetValue(selected.Command + suffix)
	m.composer.CursorEnd()
	m.closeSlashMenu()
}

func (m Model) slashMenuView() string {
	if !m.slashMenuOpen {
		return ""
	}
	var output strings.Builder
	for index, option := range m.filteredSlashOptions() {
		marker := colors.Muted.Render("  ")
		command := colors.Location.Render(option.Command)
		if index == m.slashSelected {
			marker = colors.Accent.Render("› ")
			command = colors.Selected.Render(option.Command)
		}
		fmt.Fprintf(&output, "%s%-14s %s\n", marker, command, colors.Muted.Render(option.Description))
	}
	return strings.TrimSuffix(output.String(), "\n")
}

func (m Model) skillsView() string {
	projectName := valueOr(m.runtime.Project.Name, m.runtime.RepositoryName)
	if projectName == "" {
		projectName = "current project"
	}
	var output strings.Builder
	fmt.Fprintf(&output, "%s  %s\n%s\n\n", colors.Section.Render("PROJECT SKILLS"), colors.Location.Render(projectName), divider(m.width))
	visible := m.filteredSkillStatuses()
	if m.skillFilter != "" {
		fmt.Fprintf(&output, "%s %s\n\n", colors.Muted.Render("filter"), colors.Selected.Render(m.skillFilter))
	}
	if len(m.skillStatuses) == 0 {
		fmt.Fprintf(&output, "  %s\n", colors.Muted.Render("No installed skills were discovered."))
	} else if len(visible) == 0 {
		fmt.Fprintf(&output, "  %s\n", colors.Muted.Render("No skills match this filter."))
	}
	for index, status := range visible {
		marker := colors.Success.Render("●")
		state := colors.Success.Render("available")
		if !status.Enabled {
			marker = colors.Muted.Render("○")
			state = colors.Muted.Render("hidden")
		}
		cursor := "  "
		name := colors.Location.Render(status.Name)
		if status.IsGroup {
			name = colors.Section.Render(status.Name)
			state = colors.Muted.Render(fmt.Sprintf("%d skills", status.Members)) + "  " + state
		} else if status.Group != "" {
			cursor = "    "
		}
		if index == m.skillSelected {
			cursor = colors.Accent.Render("› ")
			name = colors.Selected.Render(status.Name)
		}
		fmt.Fprintf(&output, "%s%s %s  %s\n", cursor, marker, name, state)
		if index == m.skillSelected && strings.TrimSpace(status.Description) != "" {
			description := lipgloss.Wrap(status.Description, max(20, m.width-8), " ")
			for _, line := range strings.Split(description, "\n") {
				fmt.Fprintf(&output, "      %s\n", colors.Muted.Render(line))
			}
		}
	}
	fmt.Fprintf(&output, "\n%s", colors.Muted.Render("type to filter   ↑/↓ select   space toggle skill/group   esc clear/return"))
	if m.err != nil {
		fmt.Fprintf(&output, "\n\n%s", colors.Failure.Render("Midgard needs attention: "+m.err.Error()))
	}
	return output.String()
}

func (m Model) filteredSkillStatuses() []local.SkillStatus {
	query := strings.ToLower(strings.TrimSpace(m.skillFilter))
	if query == "" {
		return m.skillStatuses
	}
	var visible []local.SkillStatus
	for _, status := range m.skillStatuses {
		if strings.Contains(strings.ToLower(status.Name), query) || strings.Contains(strings.ToLower(status.Group), query) || strings.Contains(strings.ToLower(status.Description), query) {
			visible = append(visible, status)
		}
	}
	return visible
}

func (m Model) modelsView() string {
	var output strings.Builder
	fmt.Fprintf(&output, "%s\n%s\n\n", colors.Section.Render("MODEL"), divider(m.width))
	visible := m.filteredModelOptions()
	if m.modelFilter != "" {
		fmt.Fprintf(&output, "%s %s\n\n", colors.Muted.Render("filter"), colors.Selected.Render(m.modelFilter))
	}
	if len(visible) == 0 {
		fmt.Fprintf(&output, "  %s\n", colors.Muted.Render("No models match this filter."))
	}
	lastProvider := ""
	for index, option := range visible {
		if option.Provider != lastProvider {
			if lastProvider != "" {
				output.WriteByte('\n')
			}
			fmt.Fprintf(&output, "  %s\n", colors.Section.Render(option.ProviderName))
			lastProvider = option.Provider
		}
		cursor := "    "
		name := colors.Location.Render(option.Name)
		marker := colors.Muted.Render("○")
		if option.Selected {
			marker = colors.Success.Render("●")
		}
		if index == m.modelSelected {
			cursor = colors.Accent.Render("  › ")
			name = colors.Selected.Render(option.Name)
		}
		if option.Model == "" {
			marker = colors.Failure.Render("×")
		}
		fmt.Fprintf(&output, "%s%s %s  %s\n", cursor, marker, name, colors.Accent.Render(option.Effort))
		if index == m.modelSelected && option.Description != "" {
			fmt.Fprintf(&output, "      %s\n", colors.Muted.Render(lipgloss.Wrap(option.Description, max(20, m.width-10), " ")))
		}
	}
	fmt.Fprintf(&output, "\n%s", colors.Muted.Render("type to filter   ↑/↓ model   ←/→ effort   enter use   esc return"))
	if m.err != nil {
		fmt.Fprintf(&output, "\n\n%s", colors.Failure.Render("Midgard needs attention: "+m.err.Error()))
	}
	return output.String()
}

func (m Model) filteredModelOptions() []local.ModelOption {
	query := strings.ToLower(strings.TrimSpace(m.modelFilter))
	if query == "" {
		return m.modelOptions
	}
	var options []local.ModelOption
	for _, option := range m.modelOptions {
		if strings.Contains(strings.ToLower(option.ProviderName+" "+option.Name+" "+option.Model+" "+option.Description), query) {
			options = append(options, option)
		}
	}
	return options
}

func (m Model) authView() string {
	var output strings.Builder
	fmt.Fprintf(&output, "%s\n%s\n\n", colors.Section.Render("MODEL PROVIDERS"), divider(m.width))
	for index, option := range m.authOptions {
		cursor := "  "
		name := colors.Location.Render(option.Name)
		marker, state := colors.Muted.Render("○"), colors.Muted.Render("sign in")
		if option.Authenticated {
			marker, state = colors.Success.Render("●"), colors.Success.Render("ready")
		}
		if index == m.authSelected {
			cursor, name = colors.Accent.Render("› "), colors.Selected.Render(option.Name)
		}
		fmt.Fprintf(&output, "%s%s %s  %s\n", cursor, marker, name, state)
		if index == m.authSelected {
			fmt.Fprintf(&output, "    %s\n", colors.Muted.Render(option.Detail))
		}
	}
	fmt.Fprintf(&output, "\n%s", colors.Muted.Render("↑/↓ provider   enter authenticate   esc return"))
	if m.err != nil {
		fmt.Fprintf(&output, "\n\n%s", colors.Failure.Render("Midgard needs attention: "+m.err.Error()))
	}
	return output.String()
}

func (m Model) environmentsView() string {
	projectName := valueOr(m.runtime.Project.Name, m.runtime.RepositoryName)
	if projectName == "" {
		projectName = "current project"
	}
	var output strings.Builder
	fmt.Fprintf(&output, "%s  %s\n%s\n\n", colors.Section.Render("RUNTIME ENVIRONMENT"), colors.Location.Render(projectName), divider(m.width))
	visible := m.filteredEnvironmentOptions()
	if m.environmentFilter != "" {
		fmt.Fprintf(&output, "%s %s\n\n", colors.Muted.Render("filter"), colors.Selected.Render(m.environmentFilter))
	}
	if len(m.environmentOptions) == 0 {
		fmt.Fprintf(&output, "  %s\n", colors.Muted.Render("No runtime environments are configured."))
	} else if len(visible) == 0 {
		fmt.Fprintf(&output, "  %s\n", colors.Muted.Render("No environments match this filter."))
	}
	for index, option := range visible {
		marker := colors.Muted.Render("○")
		state := ""
		if option.Active {
			marker = colors.Success.Render("●")
			state = colors.Success.Render("active")
		}
		cursor := "  "
		name := colors.Location.Render(option.Name)
		if index == m.environmentSelected {
			cursor = colors.Accent.Render("› ")
			name = colors.Selected.Render(option.Name)
		}
		fmt.Fprintf(&output, "%s%s %s  %s\n", cursor, marker, name, state)
		if index == m.environmentSelected {
			if len(option.Variables) == 0 {
				fmt.Fprintf(&output, "      %s\n", colors.Muted.Render("No variables"))
			}
			for _, variable := range option.Variables {
				kind := variable.Kind
				if variable.Inherited {
					kind += ", inherited"
				}
				fmt.Fprintf(&output, "      %s  %s", colors.Accent.Render(variable.Name), colors.Muted.Render(kind))
				if variable.Description != "" {
					fmt.Fprintf(&output, "  — %s", colors.Muted.Render(variable.Description))
				}
				output.WriteByte('\n')
			}
		}
	}
	fmt.Fprintf(&output, "\n%s", colors.Muted.Render("type to filter   ↑/↓ select   enter use   esc clear/return"))
	if m.err != nil {
		fmt.Fprintf(&output, "\n\n%s", colors.Failure.Render("Midgard needs attention: "+m.err.Error()))
	}
	return output.String()
}

func (m Model) filteredEnvironmentOptions() []local.EnvironmentOption {
	query := strings.ToLower(strings.TrimSpace(m.environmentFilter))
	if query == "" {
		return m.environmentOptions
	}
	var visible []local.EnvironmentOption
	for _, option := range m.environmentOptions {
		matches := strings.Contains(strings.ToLower(option.Name), query)
		for _, variable := range option.Variables {
			matches = matches || strings.Contains(strings.ToLower(variable.Name), query) || strings.Contains(strings.ToLower(variable.Description), query)
		}
		if matches {
			visible = append(visible, option)
		}
	}
	return visible
}

func (m Model) chatContent() string {
	var output strings.Builder
	if m.omittedMessages > 0 || m.omittedActivities > 0 {
		var details []string
		if m.omittedMessages > 0 {
			details = append(details, fmt.Sprintf("%d older messages", m.omittedMessages))
		}
		if m.omittedActivities > 0 {
			details = append(details, fmt.Sprintf("%d older activities", m.omittedActivities))
		}
		fmt.Fprintf(&output, "\n%s\n", colors.Muted.Render("… "+strings.Join(details, " and ")+" are not rendered; complete history remains saved"))
	}
	entries := make([]chatTimelineEntry, 0, len(m.messages)+len(m.toolOrder)+len(m.interruptions)+len(m.controls))
	for index := range m.messages {
		message := &m.messages[index]
		if message.Sequence > 0 {
			entries = append(entries, chatTimelineEntry{Sequence: message.Sequence, Message: message})
		}
	}
	for _, id := range m.toolOrder {
		card := m.tools[id]
		if card != nil && card.Sequence > 0 {
			entries = append(entries, chatTimelineEntry{Sequence: card.Sequence, Tool: card})
		}
	}
	for index := range m.interruptions {
		interruption := &m.interruptions[index]
		if interruption.Sequence > 0 {
			entries = append(entries, chatTimelineEntry{Sequence: interruption.Sequence, Interruption: interruption})
		}
	}
	for id, sequence := range m.controlSequences {
		if sequence > 0 && m.controlContent[id] != "" {
			entries = append(entries, chatTimelineEntry{Sequence: sequence, ControlID: id})
		}
	}
	sort.SliceStable(entries, func(left, right int) bool { return entries[left].Sequence < entries[right].Sequence })
	for _, entry := range entries {
		switch {
		case entry.Message != nil:
			fmt.Fprintf(&output, "\n%s\n", renderChatMessage(entry.Message.Role, entry.Message.Content, m.viewport.Width()-2))
		case entry.Tool != nil:
			renderToolCard(&output, entry.Tool, m.viewport.Width()-2)
		case entry.Interruption != nil:
			renderInterruption(&output, *entry.Interruption)
		case entry.ControlID != "":
			renderSteering(&output, m.controls[entry.ControlID], m.controlContent[entry.ControlID], m.viewport.Width()-2)
		}
	}

	// During a live turn the optimistic user row and the first action activity
	// can arrive a frame before their durable sequence notification. Keep that
	// short gap readable without changing the ordering of replayed history.
	renderedTools := make(map[string]bool, len(m.toolOrder))
	renderUnsequencedToolsForTurn := func(turnID string) {
		for _, id := range m.toolOrder {
			card := m.tools[id]
			if renderedTools[id] || card == nil || card.Sequence != 0 || card.TurnID != turnID {
				continue
			}
			renderToolCard(&output, card, m.viewport.Width()-2)
			renderedTools[id] = true
		}
	}
	for _, message := range m.messages {
		if message.Sequence != 0 {
			continue
		}
		if message.Role == "assistant" {
			renderUnsequencedToolsForTurn(message.TurnID)
		}
		fmt.Fprintf(&output, "\n%s\n", renderChatMessage(message.Role, message.Content, m.viewport.Width()-2))
	}
	for _, id := range m.toolOrder {
		card := m.tools[id]
		if card != nil && card.Sequence == 0 && !renderedTools[id] {
			renderToolCard(&output, card, m.viewport.Width()-2)
		}
	}
	ids := make([]string, 0, len(m.controls))
	for id := range m.controls {
		if m.controlSequences[id] == 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		renderSteering(&output, m.controls[id], m.controlContent[id], m.viewport.Width()-2)
	}
	if m.activeModelState != nil {
		if rendered := renderBragiCard(*m.activeModelState); rendered != "" {
			fmt.Fprintf(&output, "\n%s", lipgloss.Wrap(rendered, max(20, m.viewport.Width()-2), " "))
		}
	} else if m.running && m.status != "" {
		fmt.Fprintf(&output, "\n%s  %s\n", m.progress.View(), m.renderCurrentStatus())
	}
	return output.String()
}

func renderSteering(output *strings.Builder, state, content string, width int) {
	steering := colors.Warning.Render("[steer "+state+"]") + " " + content
	fmt.Fprintf(output, "\n%s\n", renderChatMessage("user", steering, width))
}

func (m *Model) pruneToolCards() {
	for len(m.toolOrder) > tuiToolCardLimit {
		remove := -1
		for index, id := range m.toolOrder {
			card := m.tools[id]
			if card == nil || card.State == "succeeded" || card.State == "failed" || card.State == "invalid" || card.State == "superseded" {
				remove = index
				break
			}
		}
		if remove < 0 {
			return
		}
		delete(m.tools, m.toolOrder[remove])
		m.toolOrder = append(m.toolOrder[:remove], m.toolOrder[remove+1:]...)
		m.omittedActivities++
	}
}

func renderInterruption(output *strings.Builder, interruption session.Interruption) {
	fmt.Fprintf(output, "\n%s\n", colors.Failure.Render("× Turn interrupted"))
	if interruption.UnknownOutcome {
		fmt.Fprintf(output, "  %s\n", colors.Warning.Render("A command was already running, so its outcome is unknown. Inspect the repository state before continuing."))
		return
	}
	fmt.Fprintf(output, "  %s\n", colors.Muted.Render("Work stopped before a final response was produced."))
}

func renderToolCard(output *strings.Builder, card *ToolCard, width int) {
	terminal := card.State == "succeeded" || card.State == "failed" || card.State == "invalid"
	if terminal && renderCompletedActivity(output, card, width) {
		return
	}
	fmt.Fprintf(output, "\n%s  %s  %s\n", styledToolState(card.State), colors.Accent.Render(card.Name), colors.Muted.Render(card.Elapsed.Round(time.Millisecond).String()))
	if !terminal {
		fmt.Fprintf(output, "  %s\n", renderActionRail(card.State))
	}
}

func shortID(value string) string {
	if len(value) <= 14 {
		return value
	}
	return value[:14]
}

func bounded(value string, limit int) string {
	if len(value) > limit {
		return value[:limit] + "…"
	}
	return value
}

func Run(ctx context.Context, runtime *local.Runtime, initialTask string) error {
	_, err := tea.NewProgram(New(ctx, runtime, initialTask)).Run()
	return err
}
