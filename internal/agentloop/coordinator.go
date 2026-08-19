package agentloop

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"midgard/internal/action"
	"midgard/internal/artifact"
	contextview "midgard/internal/context"
	runtimeenv "midgard/internal/environment"
	"midgard/internal/eventlog"
	"midgard/internal/observe"
	"midgard/internal/policy"
	modelprotocol "midgard/internal/protocol"
	"midgard/internal/provider"
	"midgard/internal/session"
	"midgard/internal/workspace"
)

type Coordinator struct {
	Provider         provider.Provider
	Artifacts        *artifact.Store
	Sessions         session.Service
	Actions          action.Service
	Observe          observe.Service
	Runner           workspace.Runner
	Runners          map[string]workspace.Runner
	Context          contextview.View
	Contexts         map[string]contextview.View
	Configuration    policy.Configuration
	RepositoryChecks map[string][][]string
	Environment      *runtimeenv.Snapshot
	Skills           policy.SkillCatalog
	Policy           policy.Policy
	Activity         ActivitySink
	// MaxProviderCalls overrides the policy turn budget when non-zero.
	MaxProviderCalls int
}

// systemPromptSource is deliberately source-controlled for review. It is
// embedded so the prompt used by an installed binary has the exact revision
// tested with that binary.
//
//go:embed prompts/system.md
var systemPromptSource string

var systemPromptTemplate = template.Must(template.New("system.md").Option("missingkey=error").Parse(systemPromptSource))

const maxSystemPromptBytes = 4_000

const (
	maxConsecutiveLengthWithoutOutput = 1
	lengthWithoutOutputRecovery       = "Your previous response reached its output limit before producing any accepted protocol lines. Do not resume private reasoning. Using the evidence already available, emit one concise complete tool action: start with + @id tool, add name/arguments/reason fields, then commit it with ! @id; or emit the final message and completion."
)

type systemPromptData struct {
	Protocol           string
	Profile            string
	ProfileVersion     string
	ProfileFingerprint string
	Objective          string
	Repositories       string
	GuidanceIndex      string
}

type Result struct {
	SessionID            string
	TurnID               string
	FinalResponse        string
	Worktree             string
	Diff                 string
	Decision             policy.CompletionDecision
	Actions              int
	ProviderCalls        int
	Model                string
	InputTokens          int64
	CacheHitInputTokens  int64
	CacheMissInputTokens int64
	OutputTokens         int64
	ThinkingTokens       int64
	ProviderDuration     time.Duration
	PeakContextTokens    int64
	ContextLimitTokens   int64
	Compactions          int
}

type generation struct {
	Stop               provider.Stop
	Actions            []modelprotocol.HostAction
	FinalMessages      []string
	CompletionProposed bool
	ProtocolFeedback   string
}

func Capabilities() action.CapabilitySet {
	return action.CapabilitySet{
		"environment.describe": validateNoArguments,
		"skill.search":         validateSkillSearch,
		"skill.read":           validateSkillRead,
		"repo.search":          validateRepositorySearch,
		"browser.run":          validateBrowserRun,
		"file.inspect":         func(raw json.RawMessage) error { return requireString(raw, "path") },
		"file.replace":         validateFileReplace,
		"patch.apply":          func(raw json.RawMessage) error { return requireString(raw, "patch") },
		"git.diff":             func(json.RawMessage) error { return nil },
		"check.run":            validateArgv,
		"shell":                validateShell,
		"shell.poll":           func(raw json.RawMessage) error { return requireString(raw, "job_id") },
		"shell.stop":           func(raw json.RawMessage) error { return requireString(raw, "job_id") },
	}
}

func (c Coordinator) Run(ctx context.Context, sessionID, objective string) (result Result, runErr error) {
	result, err := c.RunTurn(ctx, sessionID, objective)
	if err != nil {
		return result, err
	}
	if _, err := c.Sessions.Finish(ctx, sessionID, "completed", "completion evidence accepted"); err != nil {
		return result, err
	}
	return result, nil
}

// RunTurn executes one conversational turn and leaves the session active so a
// local client can submit follow-up turns in the same worktree.
func (c Coordinator) RunTurn(ctx context.Context, sessionID, objective string) (result Result, runErr error) {
	if c.Provider == nil || c.Artifacts == nil || c.Sessions.Log == nil || c.Actions.Log == nil || c.Policy == nil {
		return Result{}, errors.New("coordinator dependencies are incomplete")
	}
	if c.Configuration.Budget.MaxWallTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Configuration.Budget.MaxWallTime)
		defer cancel()
	}
	turnID := randomID("turn")
	primary := c.primaryRunner()
	result = Result{SessionID: sessionID, TurnID: turnID, Worktree: primary.Binding.WorktreeRoot}
	if _, err := c.Sessions.StartTurn(ctx, sessionID, turnID); err != nil {
		return result, err
	}
	c.emit(Activity{Kind: "turn", SessionID: sessionID, TurnID: turnID, State: "started", Message: "turn started"})
	turnActive := true
	defer func() {
		if runErr != nil && turnActive {
			outcome := "failed"
			if ctx.Err() != nil {
				outcome = "interrupted"
			}
			message := "turn " + outcome
			if outcome == "failed" {
				failure := summarizeTurnFailure(runErr)
				if _, err := c.Sessions.FailTurn(context.Background(), sessionID, turnID, failure); err != nil {
					_, _ = c.Sessions.EndTurn(context.Background(), sessionID, turnID, outcome)
				}
				message = failure.Message
			} else {
				_, _ = c.Sessions.EndTurn(context.Background(), sessionID, turnID, outcome)
			}
			c.emit(Activity{Kind: "turn", SessionID: sessionID, TurnID: turnID, State: outcome, Message: message})
		}
	}()
	userMessage, err := c.Sessions.RecordMessage(ctx, sessionID, turnID, "user", objective)
	if err != nil {
		return result, err
	}
	c.emit(Activity{Kind: "message", SessionID: sessionID, TurnID: turnID, Sequence: userMessage.Sequence, Role: "user", Message: objective})

	messages, err := c.initialMessages(ctx, sessionID, objective)
	if err != nil {
		return result, err
	}
	contextBudget := c.Configuration.Budget.Context
	contextWindow := newContextWindow(messages, objective)
	result.ContextLimitTokens = contextBudget.LimitTokens
	protocolTurn, err := modelprotocol.NewTurn()
	if err != nil {
		return result, err
	}
	maxActions := c.Configuration.Budget.MaxActions
	if maxActions <= 0 {
		maxActions = 100
	}
	maxProviderCalls := c.MaxProviderCalls
	if maxProviderCalls <= 0 {
		maxProviderCalls = c.Configuration.Budget.MaxTurns
	}
	if maxProviderCalls <= 0 {
		maxProviderCalls = 24
	}
	consecutivePatchFailures := 0
	consecutiveLengthWithoutOutput := 0
	directAdvisory := isDirectAdvisoryObjective(objective)
	directInformational := isDirectInformationalObjective(objective)
	for result.Actions < maxActions {
		if _, err := c.applySteers(ctx, sessionID, turnID, &messages); err != nil {
			return result, err
		}
		compacted, compaction, err := contextWindow.compact(messages, contextBudget)
		if err != nil {
			return result, err
		}
		if compaction.Removed > 0 {
			messages = compacted
			result.Compactions++
			payload, _ := json.Marshal(map[string]any{"turn_id": turnID, "before_tokens": compaction.BeforeTokens,
				"after_tokens": compaction.AfterTokens, "removed_messages": compaction.Removed, "quality_limit_tokens": contextBudget.LimitTokens})
			if c.Observe.Log != nil {
				if _, err := c.Observe.RecordEvidence(ctx, sessionID, observe.Evidence{Kind: "context.compaction", Payload: payload}); err != nil {
					return result, err
				}
			}
			c.emit(Activity{Kind: "context", SessionID: sessionID, TurnID: turnID, State: "compacted",
				Message:       fmt.Sprintf("working context compacted from %d to %d tokens", compaction.BeforeTokens, compaction.AfterTokens),
				ContextTokens: compaction.AfterTokens, ContextLimitTokens: contextBudget.LimitTokens, ContextEstimated: true, Compactions: result.Compactions})
		}
		contextTokens := contextWindow.estimate(messages)
		if contextBudget.LimitTokens > 0 && contextTokens > contextBudget.LimitTokens {
			return result, fmt.Errorf("working context needs about %d tokens after compaction, above Midgard's %d-token quality limit; start a new chat with a concise handoff", contextTokens, contextBudget.LimitTokens)
		}
		result.PeakContextTokens = max(result.PeakContextTokens, contextTokens)
		if result.ProviderCalls >= maxProviderCalls {
			return result, fmt.Errorf("provider call budget of %d exhausted", maxProviderCalls)
		}
		result.ProviderCalls++
		c.emit(Activity{Kind: "provider", SessionID: sessionID, TurnID: turnID, State: "running", Message: "provider request", ProviderCalls: result.ProviderCalls, Actions: result.Actions,
			InputTokens: result.InputTokens, CacheHitInputTokens: result.CacheHitInputTokens, CacheMissInputTokens: result.CacheMissInputTokens, OutputTokens: result.OutputTokens,
			ContextTokens: contextTokens, ContextLimitTokens: contextBudget.LimitTokens, ContextEstimated: true, Compactions: result.Compactions})
		requestMessageCount := len(messages)
		providerStarted := time.Now()
		generated, err := c.generate(ctx, sessionID, turnID, messages, protocolTurn)
		result.ProviderDuration += time.Since(providerStarted)
		if err != nil {
			return result, err
		}
		stop := generated.Stop
		if stop.Model != "" {
			result.Model = stop.Model
		}
		result.InputTokens += stop.InputTokens
		result.CacheHitInputTokens += stop.CacheHitInputTokens
		result.CacheMissInputTokens += stop.CacheMissInputTokens
		result.OutputTokens += stop.OutputTokens
		result.ThinkingTokens += stop.ThinkingTokens
		contextWindow.recordRequest(requestMessageCount, stop.InputTokens)
		contextTokens = stop.InputTokens
		result.PeakContextTokens = max(result.PeakContextTokens, contextTokens)
		c.emit(Activity{Kind: "provider", SessionID: sessionID, TurnID: turnID, State: "completed", Message: "provider response", ProviderCalls: result.ProviderCalls, Actions: result.Actions,
			InputTokens: result.InputTokens, CacheHitInputTokens: result.CacheHitInputTokens, CacheMissInputTokens: result.CacheMissInputTokens, OutputTokens: result.OutputTokens, ThinkingTokens: result.ThinkingTokens,
			ProviderDuration: result.ProviderDuration, ContextTokens: contextTokens, ContextLimitTokens: contextBudget.LimitTokens, Compactions: result.Compactions})
		if lengthWithoutBragiOutput(generated) {
			consecutiveLengthWithoutOutput++
			if c.Observe.Log != nil {
				payload, _ := json.Marshal(map[string]any{
					"provider_call":   result.ProviderCalls,
					"reason":          stop.Reason,
					"thinking_tokens": stop.ThinkingTokens,
				})
				if _, err := c.Observe.RecordEvidence(ctx, sessionID, observe.Evidence{Kind: "provider.length_without_output", Payload: payload}); err != nil {
					return result, err
				}
			}
			if consecutiveLengthWithoutOutput > maxConsecutiveLengthWithoutOutput {
				return result, errors.New("the provider reached its response limit twice without producing accepted protocol lines")
			}
			// Do not append the empty assistant message or its provider replay state:
			// the prior request exhausted its budget in private reasoning and offers
			// no accepted protocol progress to preserve.
			messages = append(messages, provider.Message{Role: "user", Content: lengthWithoutOutputRecovery})
			continue
		}
		consecutiveLengthWithoutOutput = 0
		messages = append(messages, stop.Message)
		pending, err := c.hasPendingSteers(ctx, sessionID)
		if err != nil {
			return result, err
		}
		if pending {
			messages = append(messages, bragiSupersededMessages(generated.Actions)...)
			if _, err := c.applySteers(ctx, sessionID, turnID, &messages); err != nil {
				return result, err
			}
			continue
		}
		if len(generated.Actions) > 0 {
			superseded := false
			for index, proposed := range generated.Actions {
				if result.Actions >= maxActions {
					return result, fmt.Errorf("action budget of %d exhausted", maxActions)
				}
				call := provider.ToolCall{ID: strings.TrimPrefix(proposed.EntityID, "@"), Name: proposed.Name, Arguments: proposed.Arguments}
				toolResult, err := c.executeTool(ctx, sessionID, turnID, call)
				if errors.Is(err, action.ErrSteeringPending) {
					messages = append(messages, bragiSupersededMessages(generated.Actions[index:])...)
					if _, applyErr := c.applySteers(ctx, sessionID, turnID, &messages); applyErr != nil {
						return result, applyErr
					}
					superseded = true
					break
				}
				if err != nil {
					return result, err
				}
				contextWindow.recordAction(proposed, toolResult)
				result.Actions++
				if call.Name == "patch_apply" {
					var output workspace.Output
					if json.Unmarshal(toolResult, &output) == nil && (output.ExitCode != 0 || output.ErrorCode != "") {
						consecutivePatchFailures++
						if consecutivePatchFailures >= 2 {
							toolResult = addToolGuidance(toolResult, "patch_apply has failed repeatedly; use file_inspect and then file_replace with the returned sha256")
						}
					} else {
						consecutivePatchFailures = 0
					}
				}
				messages = append(messages, provider.Message{Role: "user", Content: bragiToolResult(proposed, toolResult)})
				if directAdvisory && index == len(generated.Actions)-1 {
					messages = append(messages, provider.Message{Role: "user", Content: "This is a direct advisory question. Use the observation above to make the concise recommendation now. Do not request another tool unless the result makes an honest answer impossible; emit the final message and completion using the protocol syntax in your next response."})
				}
				pending, err := c.hasPendingSteers(ctx, sessionID)
				if err != nil {
					return result, err
				}
				if pending {
					messages = append(messages, bragiSupersededMessages(generated.Actions[index+1:])...)
					if _, err := c.applySteers(ctx, sessionID, turnID, &messages); err != nil {
						return result, err
					}
					superseded = true
					break
				}
			}
			if superseded {
				continue
			}
			if generated.ProtocolFeedback != "" {
				messages = append(messages, provider.Message{Role: "user", Content: generated.ProtocolFeedback})
			}
			continue
		}
		if generated.ProtocolFeedback != "" {
			messages = append(messages, provider.Message{Role: "user", Content: generated.ProtocolFeedback})
			continue
		}
		if !generated.CompletionProposed || len(generated.FinalMessages) == 0 {
			messages = append(messages, provider.Message{Role: "user", Content: "No executable tool or complete final response was accepted. Use the protocol syntax above. To finish, create and commit an assistant-to-user final message, then create and commit a completion entity."})
			continue
		}
		result.FinalResponse = strings.Join(generated.FinalMessages, "\n")

		researched, err := c.Actions.HasSucceededInTurn(ctx, sessionID, turnID,
			"environment.describe", "skill.search", "skill.read", "repo.search", "file.inspect", "git.diff", "browser.run")
		if err != nil {
			return result, err
		}
		sourceChangedThisTurn, err := c.Actions.HasSucceededInTurn(ctx, sessionID, turnID,
			"file.replace", "patch.apply", "shell")
		if err != nil {
			return result, err
		}
		researchResponse := !sourceChangedThisTurn && researched
		advisoryResponse := directAdvisory && !sourceChangedThisTurn
		informationalResponse := directInformational && !sourceChangedThisTurn
		nonImplementationResponse := researchResponse || advisoryResponse || informationalResponse
		var diff string
		if !nonImplementationResponse {
			diff, err = c.collectGitDiffEvidence(ctx, sessionID, turnID, &result)
			if errors.Is(err, action.ErrSteeringPending) {
				result.FinalResponse = ""
				if _, applyErr := c.applySteers(ctx, sessionID, turnID, &messages); applyErr != nil {
					return result, applyErr
				}
				continue
			}
			if err != nil {
				return result, err
			}
			result.Diff = diff
		}
		hasDiff := strings.TrimSpace(diff) != ""
		var checks []policy.CheckEvidence
		if !nonImplementationResponse {
			checks, err = c.collectRequiredCheckEvidence(ctx, sessionID, turnID, &result)
			if errors.Is(err, action.ErrSteeringPending) {
				result.FinalResponse = ""
				if _, applyErr := c.applySteers(ctx, sessionID, turnID, &messages); applyErr != nil {
					return result, applyErr
				}
				continue
			}
			if err != nil {
				return result, err
			}
		}
		terminal, err := c.actionsTerminal(ctx, sessionID)
		if err != nil {
			return result, err
		}
		verifiedNoOp := !hasDiff && generated.CompletionProposed && len(generated.FinalMessages) > 0 && allChecksPass(checks)
		evidence := policy.CompletionEvidence{
			ObjectiveAddressed:    (hasDiff && allChecksPass(checks)) || verifiedNoOp || researchResponse || advisoryResponse || informationalResponse,
			GitDiffObserved:       hasDiff,
			VerifiedNoOp:          verifiedNoOp,
			ResearchResponse:      researchResponse,
			AdvisoryResponse:      advisoryResponse,
			InformationalResponse: informationalResponse,
			SourceChangedThisTurn: sourceChangedThisTurn,
			ActionsTerminal:       terminal,
			Checks:                checks,
		}
		result.Decision = c.Policy.EvaluateCompletion(evidence)
		decisionRaw, _ := json.Marshal(result.Decision)
		if _, err := c.Observe.RecordEvidence(ctx, sessionID, observe.Evidence{Kind: "completion.decision", Payload: decisionRaw}); err != nil {
			return result, err
		}
		if !result.Decision.Complete {
			return result, fmt.Errorf("completion gate rejected the turn: %s", strings.Join(result.Decision.Reasons, "; "))
		}
		if result.Model != "" && result.InputTokens+result.OutputTokens > 0 {
			if _, err := c.Sessions.RecordTurnUsage(ctx, sessionID, session.TurnUsage{
				TurnID: turnID, Model: result.Model, InputTokens: result.InputTokens,
				CacheHitInputTokens: result.CacheHitInputTokens, CacheMissInputTokens: result.CacheMissInputTokens,
				OutputTokens: result.OutputTokens, ThinkingTokens: result.ThinkingTokens, ProviderDurationMillis: result.ProviderDuration.Milliseconds(), PeakContextTokens: result.PeakContextTokens,
				ContextLimitTokens: result.ContextLimitTokens, Compactions: result.Compactions,
			}); err != nil {
				return result, err
			}
		}
		if _, err := c.Sessions.RecordMessage(ctx, sessionID, turnID, "assistant", result.FinalResponse); err != nil {
			return result, err
		}
		if _, err := c.Sessions.EndTurn(ctx, sessionID, turnID, "completed"); err != nil {
			return result, err
		}
		turnActive = false
		c.emit(Activity{Kind: "final", SessionID: sessionID, TurnID: turnID, State: "completed", Message: result.FinalResponse, ProviderCalls: result.ProviderCalls, Actions: result.Actions})
		return result, nil
	}
	return result, fmt.Errorf("action budget of %d exhausted", maxActions)
}

func (c Coordinator) actionsTerminal(ctx context.Context, sessionID string) (bool, error) {
	var count int
	err := c.Actions.Log.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM action_projection
WHERE session_id=? AND state NOT IN ('rejected','retracted','succeeded','failed','compensation_committed')`, sessionID).Scan(&count)
	return count == 0, err
}

func (c Coordinator) initialMessages(ctx context.Context, sessionID, objective string) ([]provider.Message, error) {
	transcript, err := c.Sessions.Messages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	systemPrompt, err := c.systemPrompt(objective)
	if err != nil {
		return nil, err
	}
	messages := []provider.Message{{Role: "system", Content: systemPrompt}}
	for _, message := range transcript {
		messages = append(messages, provider.Message{Role: message.Role, Content: message.Content})
	}
	return messages, nil
}

// isDirectAdvisoryObjective identifies requests whose outcome is a bounded
// recommendation rather than a source change or factual report. The feature
// policy, not model wording, uses this only to permit a no-edit completion.
func isDirectAdvisoryObjective(objective string) bool {
	value := strings.ToLower(strings.TrimSpace(objective))
	for _, marker := range []string{
		"implement ", "add ", "change ", "edit ", "fix ", "build ", "create ", "remove ", "delete ",
	} {
		if strings.Contains(value, marker) {
			return false
		}
	}
	for _, marker := range []string{
		"recommend", "suggest", "what should", "next step", "next steps",
		"high impact", "highest impact", "prioritize", "priority",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

// isDirectInformationalObjective identifies plain questions whose requested
// outcome is an explanation, not a repository change or implementation check.
func isDirectInformationalObjective(objective string) bool {
	value := strings.ToLower(strings.TrimSpace(objective))
	if isDirectAdvisoryObjective(value) {
		return false
	}
	for _, marker := range []string{
		"implement ", "add ", "change ", "edit ", "fix ", "build ", "create ", "remove ", "delete ",
	} {
		if strings.Contains(value, marker) {
			return false
		}
	}
	for _, prefix := range []string{
		"what is ", "what's ", "whats ", "what does ", "why ", "how ", "where ", "which ", "who ", "explain ",
	} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func (c Coordinator) systemPrompt(objective string) (string, error) {
	contexts := c.repositoryContexts()
	names := make([]string, 0, len(contexts))
	for name := range contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	var facts strings.Builder
	for _, name := range names {
		view := contexts[name]
		fmt.Fprintf(&facts, "- %s: worktree %s; start %s; default branch %s; status %s\n", name, view.Repository.Root, view.Repository.StartCommit, view.Repository.DefaultBranch, view.Repository.Status)
	}
	var guidanceIndex strings.Builder
	for _, name := range names {
		for _, item := range contexts[name].Guidance {
			fmt.Fprintf(&guidanceIndex, "- %s/%s\n", name, item.Path)
		}
	}
	bragiTurn, err := modelprotocol.NewTurn()
	if err != nil {
		return "", err
	}
	negotiation := bragiTurn.Negotiation()
	var prompt bytes.Buffer
	if err := systemPromptTemplate.Execute(&prompt, systemPromptData{
		Protocol:           negotiation.Protocol,
		Profile:            negotiation.Profile,
		ProfileVersion:     negotiation.ProfileVersion,
		ProfileFingerprint: negotiation.ProfileFingerprint,
		Objective:          objective,
		Repositories:       facts.String(),
		GuidanceIndex:      guidanceIndex.String(),
	}); err != nil {
		return "", fmt.Errorf("render system prompt: %w", err)
	}
	return prompt.String(), nil
}

type recordingSink struct {
	recorder *provider.TraceRecorder
	events   []provider.Event
	live     func(provider.LiveUpdate)
}

func (s *recordingSink) Emit(event provider.Event) error {
	if err := s.recorder.Emit(event); err != nil {
		return err
	}
	s.events = append(s.events, event)
	return nil
}

func (s *recordingSink) EmitLive(update provider.LiveUpdate) {
	if s.live != nil {
		s.live(update)
	}
}

func (c Coordinator) generate(ctx context.Context, sessionID, turnID string, messages []provider.Message, protocolTurn *modelprotocol.Turn) (generation, error) {
	protocolTurn.BeginSource()
	startEvent := protocolTurn.EventCount()
	prepared, err := c.Provider.Prepare(provider.Request{Messages: messages})
	if err != nil {
		return generation{}, err
	}
	if prepared == nil {
		return generation{}, errors.New("provider did not prepare a request")
	}
	request := prepared.RequestEvent()
	if err := request.Validate(); err != nil {
		return generation{}, fmt.Errorf("provider prepared an invalid request observation: %w", err)
	}
	if request.Sequence != 1 {
		return generation{}, fmt.Errorf("provider request observation must start at sequence 1")
	}
	requestRecorder, err := provider.NewTraceRecorder(c.Artifacts)
	if err != nil {
		return generation{}, err
	}
	if err := requestRecorder.Emit(request); err != nil {
		_ = requestRecorder.Abort()
		return generation{}, err
	}
	requestTrace, err := requestRecorder.Seal()
	if err != nil {
		return generation{}, err
	}
	requestDraft, ok := provider.Normalize(randomID("evt"), sessionID, turnID, requestTrace.Ref, request)
	if !ok || requestDraft.Kind != "provider.requested" {
		return generation{}, fmt.Errorf("provider request observation %q is not a supported request boundary", request.NativeKind)
	}
	// The exact native request artifact and its canonical boundary are durable
	// before Execute may perform provider I/O. This is the model-visible-means-
	// logged invariant at the provider boundary.
	if _, err := c.Sessions.Log.AppendCurrent(ctx, requestDraft); err != nil {
		return generation{}, err
	}
	recorder, err := provider.NewTraceRecorderAfter(c.Artifacts, request.Sequence)
	if err != nil {
		return generation{}, err
	}
	var streamed strings.Builder
	lastLiveKind := provider.LiveKind("")
	sink := &recordingSink{recorder: recorder, live: func(update provider.LiveUpdate) {
		// Stream deltas are retained in the provider trace and still feed Bragi
		// below. The TUI only needs the current phase, so publishing every token
		// would repeatedly rebuild a large terminal viewport without changing its
		// visible status.
		if update.Kind != lastLiveKind {
			c.emit(Activity{Kind: "stream", SessionID: sessionID, TurnID: turnID, State: string(update.Kind)})
			lastLiveKind = update.Kind
		}
		if update.Kind == provider.LiveOutput {
			streamed.WriteString(update.Delta)
			c.emitBragiUpdates(sessionID, turnID, protocolTurn.Write(update.Delta))
		}
	}}
	stop, err := prepared.Execute(ctx, sink)
	if err != nil {
		c.emitBragiUpdates(sessionID, turnID, protocolTurn.FinishInterrupted())
		_ = recorder.Abort()
		return generation{}, err
	}
	if len(stop.Message.ToolCalls) > 0 {
		_ = recorder.Abort()
		return generation{}, errors.New("the model returned an unsupported native tool call; this Midgard runtime accepts actions only through committed model-protocol entities")
	}
	streamedText := streamed.String()
	switch {
	case streamedText == "":
		c.emitBragiUpdates(sessionID, turnID, protocolTurn.Write(stop.Message.Content))
	case stop.Message.Content == streamedText:
	case strings.HasPrefix(stop.Message.Content, streamedText):
		c.emitBragiUpdates(sessionID, turnID, protocolTurn.Write(strings.TrimPrefix(stop.Message.Content, streamedText)))
	default:
		_ = recorder.Abort()
		return generation{}, errors.New("provider streamed content does not match its completed message")
	}
	c.emitBragiUpdates(sessionID, turnID, protocolTurn.FinishCompleted())
	if len(sink.events) == 0 {
		_ = recorder.Abort()
		return generation{}, errors.New("provider returned without a native response observation")
	}
	trace, err := recorder.Seal()
	if err != nil {
		return generation{}, err
	}
	for _, native := range sink.events {
		draft, ok := provider.Normalize(randomID("evt"), sessionID, turnID, trace.Ref, native)
		if !ok {
			continue
		}
		if _, err := c.Sessions.Log.AppendCurrent(ctx, draft); err != nil {
			return generation{}, err
		}
	}
	negotiation := protocolTurn.Negotiation()
	protocolEvents := protocolTurn.Events()
	for _, protocolEvent := range protocolEvents[startEvent:] {
		payload, err := json.Marshal(struct {
			Negotiation modelprotocol.Negotiation `json:"negotiation"`
			Event       any                       `json:"event"`
		}{Negotiation: negotiation, Event: protocolEvent})
		if err != nil {
			return generation{}, err
		}
		kind := "bragi." + strings.ReplaceAll(protocolEvent.Kind, ".", "_")
		if _, err := c.Sessions.Log.AppendCurrent(ctx, eventlog.Draft{
			EventID: randomID("evt"), SessionID: sessionID, TurnID: turnID,
			Actor: eventlog.ActorServer, Kind: kind, SchemaVersion: 1,
			Visibility: eventlog.VisibilityInternal, Payload: payload,
		}); err != nil {
			return generation{}, err
		}
	}
	if stop.Message.ReplayState != nil {
		state := stop.Message.ReplayState
		if strings.TrimSpace(state.Adapter) == "" || !json.Valid(state.Payload) {
			return generation{}, errors.New("provider returned invalid replay state")
		}
		stored, err := c.Artifacts.Put(bytes.NewReader(state.Payload))
		if err != nil {
			return generation{}, err
		}
		state.ArtifactRef = stored.Ref
		payload, _ := json.Marshal(map[string]string{"adapter": state.Adapter})
		if _, err := c.Sessions.Log.AppendCurrent(ctx, eventlog.Draft{
			EventID: randomID("evt"), SessionID: sessionID, TurnID: turnID,
			Actor: eventlog.ActorModel, Kind: "provider.replay_state", SchemaVersion: 1,
			Visibility: eventlog.VisibilityInternal, Payload: payload, ArtifactRef: stored.Ref,
		}); err != nil {
			return generation{}, err
		}
	}
	actions, err := protocolTurn.HostActionsSince(startEvent)
	if err != nil {
		return generation{}, err
	}
	generated := generation{Stop: stop, Actions: actions,
		FinalMessages:      protocolTurn.FinalMessagesSince(startEvent),
		CompletionProposed: protocolTurn.CompletionProposedSince(startEvent)}
	type protocolIssue struct {
		line    int
		code    string
		message string
		count   int
	}
	var issues []protocolIssue
	issueIndex := map[string]int{}
	for _, event := range protocolEvents[startEvent:] {
		if event.Diagnostic != nil {
			key := event.Diagnostic.Code + "\x00" + event.Diagnostic.Message
			if index, exists := issueIndex[key]; exists {
				issues[index].count++
				continue
			}
			issueIndex[key] = len(issues)
			issues = append(issues, protocolIssue{line: event.Diagnostic.Line, code: event.Diagnostic.Code, message: event.Diagnostic.Message, count: 1})
		}
	}
	if len(issues) > 0 {
		feedback := make([]string, 0, min(len(issues), 12))
		for _, issue := range issues[:min(len(issues), 12)] {
			detail := fmt.Sprintf("line %d: %s (%s)", issue.line, issue.message, issue.code)
			if issue.count > 1 {
				detail += fmt.Sprintf("; repeated %d times", issue.count)
			}
			if hint := protocolRepairHint(issue.code); hint != "" {
				detail += "; " + hint
			}
			feedback = append(feedback, detail)
		}
		if len(issues) > len(feedback) {
			feedback = append(feedback, fmt.Sprintf("%d additional diagnostic types omitted", len(issues)-len(feedback)))
		}
		generated.ProtocolFeedback = "Midgard rejected malformed protocol lines. Continue with corrected, complete LF-terminated lines. Create a new action with + @id tool, add its fields, then commit it with ! @id. Repair existing drafts only with ~ or -, and use new unique entity IDs for new actions:\n- " + strings.Join(feedback, "\n- ")
	}
	return generated, nil
}

func protocolRepairHint(code string) string {
	switch code {
	case "literal_record_required":
		return "an open literal accepts only | content lines or ! @id.field to seal it"
	case "unknown_operator":
		return "do not use XML/DSML or <…tool_calls> markup; each accepted line starts with +, ~, -, or ! followed by one space"
	case "literal_continuation_without_open_literal":
		return "open the field with + @id.field | before writing | content"
	case "target_missing", "target_entity_missing":
		return "create the entity before changing or committing its fields"
	default:
		return ""
	}
}

func lengthWithoutBragiOutput(generated generation) bool {
	return generated.Stop.Reason == "length" && strings.TrimSpace(generated.Stop.Message.Content) == "" &&
		len(generated.Actions) == 0 && len(generated.FinalMessages) == 0 && !generated.CompletionProposed
}

func summarizeTurnFailure(err error) session.TurnFailure {
	if err == nil {
		return session.TurnFailure{Code: "turn_failed", Message: "Midgard stopped before a final response was produced. Inspect the repository state before continuing."}
	}
	switch {
	case strings.Contains(err.Error(), "local Codex bridge did not respond"), strings.Contains(err.Error(), "local Codex bridge did not accept"), strings.Contains(err.Error(), "local Codex bridge did not begin the model turn"):
		return session.TurnFailure{Code: "provider_start_timeout", Message: "The local Codex connection did not start responding in time. No repository action was run; try the request again."}
	case errors.Is(err, context.DeadlineExceeded):
		return session.TurnFailure{Code: "turn_time_limit", Message: "The turn reached Midgard's time limit before a final response was produced. Inspect the repository state, then continue."}
	case strings.Contains(err.Error(), "provider reached its response limit"):
		return session.TurnFailure{Code: "provider_length_without_output", Message: "The model twice used its response limit without producing a usable action or answer. Continue with a shorter, more focused request."}
	case strings.Contains(err.Error(), "provider call budget"):
		return session.TurnFailure{Code: "provider_call_budget_exhausted", Message: "The model used Midgard's turn budget without a final response. Inspect the repository state, then continue with a focused request."}
	case strings.Contains(err.Error(), "action budget"):
		return session.TurnFailure{Code: "action_budget_exhausted", Message: "The agent used Midgard's action budget without a final response. Inspect the repository state, then continue with a focused request."}
	case strings.Contains(err.Error(), "DeepSeek API returned"), strings.Contains(err.Error(), "DeepSeek stream"):
		return session.TurnFailure{Code: "provider_response_unavailable", Message: "The model provider did not return a complete response. No new action was dispatched from that response; inspect the repository state, then continue."}
	default:
		return session.TurnFailure{Code: "turn_failed", Message: "Midgard stopped before a final response was produced. Inspect the repository state before continuing."}
	}
}

func (c Coordinator) emitBragiUpdates(sessionID, turnID string, updates []modelprotocol.Update) {
	for _, update := range updates {
		activity := Activity{Kind: "model_state", SessionID: sessionID, TurnID: turnID,
			State: update.Event.Kind, EntityID: update.Event.EntityID, Revision: update.Event.Revision}
		if update.Event.Diagnostic != nil {
			activity.Message = update.Event.Diagnostic.Message
		}
		if update.Exists {
			activity.Name = update.Entity.Type
			if update.Publishable {
				activity.Arguments = modelprotocol.EntityJSON(update.Entity)
			}
		}
		c.emit(activity)
	}
}

func (c Coordinator) executeTool(ctx context.Context, sessionID, turnID string, call provider.ToolCall) (json.RawMessage, error) {
	capability, ok := map[string]string{
		"environment_describe": "environment.describe",
		"skill_search":         "skill.search",
		"skill_read":           "skill.read",
		"repo_search":          "repo.search",
		"browser_run":          "browser.run",
		"file_inspect":         "file.inspect",
		"file_replace":         "file.replace",
		"patch_apply":          "patch.apply",
		"git_diff":             "git.diff",
		"check_run":            "check.run",
		"shell":                "shell",
		"shell_poll":           "shell.poll",
		"shell_stop":           "shell.stop",
	}[call.Name]
	if !ok {
		return nil, fmt.Errorf("provider requested unsupported tool %q", call.Name)
	}
	if c.Environment != nil && (capability == "shell" || capability == "check.run") {
		arguments, err := withEnvironmentRevision(call.Arguments, c.Environment.ID)
		if err != nil {
			return nil, err
		}
		call.Arguments = arguments
	}
	actionID := sessionID + ":" + turnID + ":" + call.ID
	intent, err := c.Actions.IntentInTurn(ctx, sessionID, turnID, actionID, capability, call.Arguments, false)
	if err != nil {
		return nil, err
	}
	c.emit(Activity{Kind: "tool", SessionID: sessionID, TurnID: turnID, Sequence: intent.LastSequence, ActionID: actionID, Name: call.Name, State: "queued", Arguments: string(call.Arguments)})
	validated, err := c.Actions.Validate(ctx, actionID)
	if err != nil {
		retracted, retractErr := c.Actions.Retract(context.Background(), actionID)
		if retractErr != nil {
			return nil, errors.Join(err, retractErr)
		}
		message := invalidToolArguments(call.Name, err)
		raw, _ := json.Marshal(map[string]any{
			"error": message, "error_code": "invalid_arguments", "executed": false,
		})
		c.emit(Activity{Kind: "tool", SessionID: sessionID, TurnID: turnID, ActionID: actionID,
			Sequence: retracted.LastSequence, Name: call.Name, State: "invalid", Arguments: string(call.Arguments), Output: string(raw), Message: message})
		return raw, nil
	}
	c.emit(Activity{Kind: "tool", SessionID: sessionID, TurnID: turnID, Sequence: validated.LastSequence, ActionID: actionID, Name: call.Name, State: "validated", Arguments: string(call.Arguments)})
	if recovery, err := c.routingRecovery(ctx, sessionID, turnID, actionID, capability, call); err != nil {
		return nil, err
	} else if recovery != nil {
		retracted, retractErr := c.Actions.Retract(context.Background(), actionID)
		if retractErr != nil {
			return nil, errors.Join(retractErr, fmt.Errorf("apply %s routing recovery", recovery.Code))
		}
		raw, _ := json.Marshal(map[string]any{
			"error": recovery.Message, "error_code": recovery.Code, "executed": false,
		})
		if err := c.recordRoutingRecovery(ctx, sessionID, turnID, call.Name, recovery.Code); err != nil {
			return nil, err
		}
		c.emit(Activity{Kind: "tool", SessionID: sessionID, TurnID: turnID, ActionID: actionID,
			Sequence: retracted.LastSequence, Name: call.Name, State: "invalid", Arguments: string(call.Arguments), Output: string(raw), Message: recovery.Message})
		return raw, nil
	}
	committed, err := c.Actions.Commit(ctx, actionID, turnID+":"+call.ID)
	if err != nil {
		if errors.Is(err, action.ErrSteeringPending) {
			retracted, _ := c.Actions.Retract(context.Background(), actionID)
			c.emit(Activity{Kind: "tool", SessionID: sessionID, TurnID: turnID, Sequence: retracted.LastSequence, ActionID: actionID, Name: call.Name, State: "superseded", Message: "superseded by steering"})
		}
		return nil, err
	}
	c.emit(Activity{Kind: "tool", SessionID: sessionID, TurnID: turnID, Sequence: committed.LastSequence, ActionID: actionID, Name: call.Name, State: "committed", Arguments: string(call.Arguments)})
	claim, err := c.Actions.Dispatch(ctx, actionID, "local-headless")
	if err != nil {
		return nil, err
	}
	c.emit(Activity{Kind: "tool", SessionID: sessionID, TurnID: turnID, ActionID: actionID, Name: call.Name, State: "running", Arguments: string(call.Arguments)})
	raw, executionErr := json.RawMessage(nil), error(nil)
	if capability == "environment.describe" {
		current, err := c.Actions.Get(ctx, claim.ActionID)
		if err != nil {
			executionErr = err
		} else if current.State != action.StateDispatched || current.Capability != capability || current.CommitID != claim.CommitID || current.DispatchOwner != claim.Owner || current.DispatchFence != claim.Fence {
			executionErr = errors.New("stale or undispatched environment action claim")
		} else {
			raw, executionErr = c.describeEnvironment()
		}
	} else if capability == "skill.search" || capability == "skill.read" {
		if c.Skills == nil {
			executionErr = errors.New("installed skills are unavailable")
		} else {
			current, err := c.Actions.Get(ctx, claim.ActionID)
			if err != nil {
				executionErr = err
			} else if current.State != action.StateDispatched || current.Capability != capability || current.CommitID != claim.CommitID || current.DispatchOwner != claim.Owner || current.DispatchFence != claim.Fence {
				executionErr = errors.New("stale or undispatched skill action claim")
			} else {
				if capability == "skill.search" {
					raw, executionErr = c.Skills.Search(call.Arguments)
				} else {
					raw, executionErr = c.Skills.Read(call.Arguments)
				}
			}
		}
	} else {
		runner, routeErr := c.runnerFor(call.Arguments)
		executionErr = routeErr
		if executionErr == nil {
			raw, executionErr = runner.Execute(ctx, claim)
		}
	}
	if executionErr != nil {
		raw, _ = json.Marshal(map[string]any{"error": executionErr.Error(), "exit_code": -1})
	}
	success := executionErr == nil
	var output workspace.Output
	if json.Unmarshal(raw, &output) == nil && output.ExitCode != 0 {
		success = false
	}
	finished, err := c.Actions.Result(ctx, claim, success, raw)
	if err != nil {
		return nil, err
	}
	state := "succeeded"
	if !success {
		state = "failed"
	}
	c.emit(Activity{Kind: "tool", SessionID: sessionID, TurnID: turnID, Sequence: finished.LastSequence, ActionID: actionID, Name: call.Name, State: state, Output: string(raw)})
	return raw, nil
}

type toolRoutingRecovery struct {
	Code    string
	Message string
}

// routingRecovery keeps high-value tool routing rules at the policy boundary:
// after basic action validation, before an action can be committed or
// dispatched. The retracted intent is evidence for the model's next repair,
// not an execution attempt.
func (c Coordinator) routingRecovery(ctx context.Context, sessionID, turnID, actionID, capability string, call provider.ToolCall) (*toolRoutingRecovery, error) {
	switch capability {
	case "browser.run":
		read, err := c.Actions.HasSucceededInTurnWithStringArgument(ctx, sessionID, turnID, "skill.read", "name", "heimdal")
		if err != nil {
			return nil, err
		}
		if !read {
			return &toolRoutingRecovery{Code: "skill_required", Message: "Read the Heimdal skill before browser automation. Run skill_read with name \"heimdal\", then repeat browser_run. Nothing was run."}, nil
		}
	case "file.replace", "patch.apply":
		if guidance := c.rootGuidancePath(call.Arguments); guidance != "" {
			read, err := c.Actions.HasSucceededInTurnWithStringArgument(ctx, sessionID, turnID, "file.inspect", "path", guidance)
			if err != nil {
				return nil, err
			}
			if !read {
				return &toolRoutingRecovery{Code: "repository_guidance_required", Message: fmt.Sprintf("Read %s before changing this repository. Run file_inspect with path %q, then repeat the edit. Nothing was changed.", guidance, guidance)}, nil
			}
		}
	case "shell":
		if !isBroadRepositoryDiscovery(call.Arguments) {
			return nil, nil
		}
		searched, err := c.Actions.HasSucceededInTurn(ctx, sessionID, turnID, "repo.search")
		if err != nil {
			return nil, err
		}
		if searched {
			return nil, nil
		}
		prior, err := c.Actions.HasPriorAttemptInTurn(ctx, sessionID, turnID, "shell", actionID)
		if err != nil {
			return nil, err
		}
		if !prior {
			return &toolRoutingRecovery{Code: "prefer_repo_search", Message: "Start broad repository discovery with repo_search so Midgard can return bounded citations. Search for the relevant words, then inspect a cited file. Nothing was run."}, nil
		}
	}
	return nil, nil
}

// rootGuidancePath returns only the repository's baseline instruction file.
// Nested instruction files remain discoverable through repo_search and are
// named in the coding prompt; their applicability depends on the target path.
func (c Coordinator) rootGuidancePath(raw json.RawMessage) string {
	contexts := c.repositoryContexts()
	name := ""
	var route struct {
		Repository string `json:"repository"`
	}
	if json.Unmarshal(raw, &route) == nil {
		name = route.Repository
	}
	if name == "" && len(contexts) == 1 {
		for candidate := range contexts {
			name = candidate
		}
	}
	view, ok := contexts[name]
	if !ok {
		return ""
	}
	for _, item := range view.Guidance {
		if item.Path == "AGENTS.md" {
			return item.Path
		}
	}
	return ""
}

func isBroadRepositoryDiscovery(raw json.RawMessage) bool {
	var arguments struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &arguments) != nil {
		return false
	}
	fields := strings.Fields(strings.TrimSpace(arguments.Command))
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "find", "fd", "fdfind", "grep", "egrep", "fgrep", "rg", "ripgrep", "ls", "tree":
		return true
	case "git":
		return len(fields) > 1 && fields[1] == "grep"
	default:
		return false
	}
}

func (c Coordinator) recordRoutingRecovery(ctx context.Context, sessionID, turnID, tool, code string) error {
	if c.Observe.Log == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]string{"turn_id": turnID, "tool": tool, "code": code})
	_, err := c.Observe.RecordEvidence(ctx, sessionID, observe.Evidence{Kind: "tool.routing_recovery", Payload: payload})
	return err
}

func invalidToolArguments(name string, validationErr error) string {
	if name == "skill_read" && strings.Contains(validationErr.Error(), "name is required") {
		return "skill_read needs arguments.name naming an installed skill. Nothing was run; correct the request and try again."
	}
	return fmt.Sprintf("%s could not be prepared because its arguments were invalid. Nothing was run; correct the request and try again.", name)
}

func withEnvironmentRevision(raw json.RawMessage, revision string) (json.RawMessage, error) {
	var arguments map[string]any
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return nil, err
	}
	arguments["_midgard_environment"] = revision
	return json.Marshal(arguments)
}

func (c Coordinator) collectGitDiffEvidence(ctx context.Context, sessionID, turnID string, result *Result) (string, error) {
	var combinedDiff strings.Builder
	for _, repository := range c.repositoryNames() {
		arguments, _ := json.Marshal(map[string]any{"repository": repository})
		rawDiff, err := c.executeTool(ctx, sessionID, turnID, provider.ToolCall{ID: randomID("evidence"), Name: "git_diff", Arguments: arguments})
		if err != nil {
			return "", err
		}
		result.Actions++
		var diffOutput workspace.Output
		if err := json.Unmarshal(rawDiff, &diffOutput); err != nil {
			return "", err
		}
		if diffOutput.Stdout != "" {
			fmt.Fprintf(&combinedDiff, "Repository: %s\n%s", repository, diffOutput.Stdout)
		}
		diffPayload, _ := json.Marshal(map[string]any{"repository": repository, "stdout": diffOutput.Stdout, "exit_code": diffOutput.ExitCode})
		if _, err := c.Observe.RecordEvidence(ctx, sessionID, observe.Evidence{Kind: "git.diff", Payload: diffPayload}); err != nil {
			return "", err
		}
	}
	return combinedDiff.String(), nil
}

func (c Coordinator) collectRequiredCheckEvidence(ctx context.Context, sessionID, turnID string, result *Result) ([]policy.CheckEvidence, error) {
	var checks []policy.CheckEvidence
	for _, repository := range c.repositoryNames() {
		checksForRepository := c.RepositoryChecks[repository]
		if checksForRepository == nil {
			checksForRepository = c.Configuration.RequiredChecks
		}
		for _, argv := range checksForRepository {
			arguments, _ := json.Marshal(map[string]any{"repository": repository, "argv": argv})
			raw, err := c.executeTool(ctx, sessionID, turnID, provider.ToolCall{ID: randomID("evidence"), Name: "check_run", Arguments: arguments})
			if err != nil {
				return nil, err
			}
			result.Actions++
			var output workspace.Output
			if err := json.Unmarshal(raw, &output); err != nil {
				return nil, err
			}
			checks = append(checks, policy.CheckEvidence{Argv: append([]string(nil), argv...), ExitCode: output.ExitCode})
			payload, _ := json.Marshal(map[string]any{"repository": repository, "argv": argv, "exit_code": output.ExitCode, "stdout": output.Stdout, "stderr": output.Stderr})
			if _, err := c.Observe.RecordEvidence(ctx, sessionID, observe.Evidence{Kind: "check.result", Payload: payload}); err != nil {
				return nil, err
			}
		}
	}
	return checks, nil
}

func (c Coordinator) primaryRunner() workspace.Runner {
	if c.Runner.Binding.WorktreeRoot != "" {
		return c.Runner
	}
	for _, name := range c.repositoryNames() {
		return c.Runners[name]
	}
	return workspace.Runner{}
}

func (c Coordinator) repositoryNames() []string {
	if len(c.Runners) == 0 {
		if c.Runner.Binding.RepositoryName != "" {
			return []string{c.Runner.Binding.RepositoryName}
		}
		return nil
	}
	names := make([]string, 0, len(c.Runners))
	for name := range c.Runners {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c Coordinator) runnerFor(arguments json.RawMessage) (workspace.Runner, error) {
	if len(c.Runners) == 0 {
		return c.Runner, nil
	}
	var route struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(arguments, &route); err != nil {
		return workspace.Runner{}, err
	}
	if route.Repository == "" && len(c.Runners) == 1 {
		return c.primaryRunner(), nil
	}
	runner, ok := c.Runners[route.Repository]
	if !ok {
		return workspace.Runner{}, fmt.Errorf("choose a repository from %s", strings.Join(c.repositoryNames(), ", "))
	}
	return runner, nil
}

func (c Coordinator) repositoryContexts() map[string]contextview.View {
	if len(c.Contexts) > 0 {
		return c.Contexts
	}
	name := c.Runner.Binding.RepositoryName
	if name == "" {
		name = "repository"
	}
	return map[string]contextview.View{name: c.Context}
}

func (c Coordinator) emit(activity Activity) {
	if c.Activity == nil {
		return
	}
	if activity.At.IsZero() {
		activity.At = time.Now()
	}
	c.Activity.EmitActivity(activity)
}

func (c Coordinator) applySteers(ctx context.Context, sessionID, turnID string, messages *[]provider.Message) (int, error) {
	controls, err := c.Sessions.PendingSteers(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	for _, control := range controls {
		*messages = append(*messages, provider.Message{Role: "user", Content: control.Content})
		if _, err := c.Sessions.AcknowledgeControl(ctx, sessionID, control.ControlID); err != nil {
			return 0, err
		}
		c.emit(Activity{Kind: "control", SessionID: sessionID, TurnID: turnID, ControlID: control.ControlID, State: "applied", Message: control.Content})
	}
	return len(controls), nil
}

func (c Coordinator) hasPendingSteers(ctx context.Context, sessionID string) (bool, error) {
	controls, err := c.Sessions.PendingSteers(ctx, sessionID)
	return len(controls) > 0, err
}

func bragiSupersededMessages(actions []modelprotocol.HostAction) []provider.Message {
	messages := make([]provider.Message, 0, len(actions))
	for _, proposed := range actions {
		messages = append(messages, provider.Message{Role: "user", Content: fmt.Sprintf(
			"Midgard host result for committed tool %s (%s):\n{\"error\":\"superseded_by_steer\",\"executed\":false}\nUse the protocol syntax above and account for the user's steering.",
			proposed.EntityID, proposed.Name)})
	}
	return messages
}

func bragiToolResult(proposed modelprotocol.HostAction, result json.RawMessage) string {
	return fmt.Sprintf("Midgard host result for committed tool %s (%s):\n%s\nUse the protocol syntax above. Do not recreate this completed tool request.",
		proposed.EntityID, proposed.Name, result)
}

func requireString(raw json.RawMessage, field string) error {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	text, ok := value[field].(string)
	if !ok || strings.TrimSpace(text) == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}

func validateArgv(raw json.RawMessage) error {
	var value struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if len(value.Argv) == 0 || strings.TrimSpace(value.Argv[0]) == "" {
		return errors.New("argv is required")
	}
	return nil
}

func validateFileReplace(raw json.RawMessage) error {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	for _, field := range []string{"path", "expected_sha256"} {
		text, ok := value[field].(string)
		if !ok || strings.TrimSpace(text) == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	if _, ok := value["content"].(string); !ok {
		return errors.New("content is required")
	}
	return nil
}

func validateShell(raw json.RawMessage) error {
	var value struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		Background     bool   `json:"background"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if strings.TrimSpace(value.Command) == "" {
		return errors.New("command is required")
	}
	if value.TimeoutSeconds < 0 || value.TimeoutSeconds > 600 {
		return errors.New("timeout_seconds must be between 1 and 600 when provided")
	}
	if value.Background && value.TimeoutSeconds != 0 {
		return errors.New("background shell work cannot also set timeout_seconds; stop it with shell_stop")
	}
	return nil
}

func validateRepositorySearch(raw json.RawMessage) error {
	var value struct {
		Query string `json:"query"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if strings.TrimSpace(value.Query) == "" {
		return errors.New("query is required")
	}
	if len(value.Query) > 500 {
		return errors.New("query must be at most 500 characters")
	}
	if value.Path == "" {
		return nil
	}
	clean := filepath.Clean(value.Path)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("path must stay inside the repository")
	}
	return nil
}

func validateBrowserRun(raw json.RawMessage) error {
	var value struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if len(value.Command) == 0 || len(value.Command) > 4_000 {
		return errors.New("browser command is required and must be at most 4000 characters")
	}
	arguments, err := workspace.ParseCommand(value.Command)
	if err != nil {
		return fmt.Errorf("browser command: %w", err)
	}
	if len(arguments) > 80 {
		return errors.New("browser command has too many arguments")
	}
	if value.TimeoutSeconds < 0 || value.TimeoutSeconds > 600 {
		return errors.New("timeout_seconds must be between 1 and 600 when provided")
	}
	switch arguments[0] {
	case "doctor", "run", "list", "session", "sessions", "report", "runs", "trace", "gc", "metadata", "signal":
	default:
		return errors.New("browser command must be a supported Heimdal QA operation")
	}
	for _, argument := range arguments {
		if argument == "--dir" || strings.HasPrefix(argument, "--dir=") {
			return errors.New("Midgard chooses the browser worktree; omit --dir")
		}
	}
	return nil
}

func validateSkillRead(raw json.RawMessage) error {
	var value struct {
		Name      string `json:"name"`
		Resource  string `json:"resource"`
		Query     string `json:"query"`
		StartLine int    `json:"start_line"`
		LineCount int    `json:"line_count"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if strings.TrimSpace(value.Name) == "" {
		return errors.New("name is required")
	}
	if len(value.Query) > 500 {
		return errors.New("query must be at most 500 characters")
	}
	if strings.TrimSpace(value.Query) != "" {
		if value.StartLine != 0 || value.LineCount != 0 {
			return errors.New("query cannot be combined with line bounds")
		}
		return nil
	}
	resource := filepath.ToSlash(strings.TrimSpace(value.Resource))
	cleanResource := filepath.Clean(strings.TrimSpace(value.Resource))
	if resource != "" && (filepath.IsAbs(cleanResource) || cleanResource == ".." || strings.HasPrefix(cleanResource, ".."+string(filepath.Separator))) {
		return errors.New("resource must stay inside the installed skill")
	}
	if resource == "" || resource == "SKILL.md" {
		if value.StartLine != 0 || value.LineCount != 0 {
			return errors.New("SKILL.md must be read completely without line bounds")
		}
		return nil
	}
	if value.StartLine <= 0 || value.LineCount <= 0 || value.LineCount > 120 {
		return errors.New("reference reads require start_line and line_count between 1 and 120; search with query first")
	}
	return nil
}

func validateSkillSearch(raw json.RawMessage) error {
	var value struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if strings.TrimSpace(value.Query) == "" {
		return errors.New("query is required")
	}
	if len(value.Query) > 500 {
		return errors.New("query must be at most 500 characters")
	}
	return nil
}

func validateNoArguments(raw json.RawMessage) error {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	if len(values) != 0 {
		return errors.New("this action does not accept arguments")
	}
	return nil
}

func (c Coordinator) describeEnvironment() (json.RawMessage, error) {
	if c.Environment == nil {
		return json.RawMessage(`{"selected":false,"variables":[]}`), nil
	}
	type variable struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Kind        string `json:"kind"`
	}
	variables := make([]variable, 0, len(c.Environment.Variables))
	for _, item := range c.Environment.Variables {
		kind := "plain"
		if item.Secret {
			kind = "secret"
		}
		variables = append(variables, variable{Name: item.Name, Description: item.Description, Kind: kind})
	}
	return json.Marshal(map[string]any{"selected": true, "name": c.Environment.Name, "variables": variables})
}

func addToolGuidance(raw json.RawMessage, guidance string) json.RawMessage {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	value["midgard_guidance"] = guidance
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return encoded
}

func allChecksPass(checks []policy.CheckEvidence) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		if check.ExitCode != 0 {
			return false
		}
	}
	return true
}

func randomID(prefix string) string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(data[:])
}
