package agentloop

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"midgard/internal/policy"
	modelprotocol "midgard/internal/protocol"
	"midgard/internal/provider"
	"midgard/internal/workspace"
)

type contextJournalEntry struct {
	EntityID string
	Name     string
	Reason   string
	Summary  string
	Success  bool
}

type contextWindow struct {
	base             []provider.Message
	objective        string
	journal          []contextJournalEntry
	lastRequestCount int
	lastExactTokens  int64
}

type contextCompaction struct {
	BeforeTokens int64
	AfterTokens  int64
	Removed      int
}

func newContextWindow(messages []provider.Message, objective string) *contextWindow {
	baseCount := 0
	for baseCount < len(messages) && messages[baseCount].Role == "system" {
		baseCount++
	}
	return &contextWindow{base: slices.Clone(messages[:baseCount]), objective: objective}
}

func (w *contextWindow) estimate(messages []provider.Message) int64 {
	if w.lastRequestCount > 0 && w.lastExactTokens > 0 && w.lastRequestCount <= len(messages) {
		return w.lastExactTokens + estimateProviderMessages(messages[w.lastRequestCount:])
	}
	return estimateProviderMessages(messages)
}

func (w *contextWindow) recordRequest(messageCount int, exactTokens int64) {
	w.lastRequestCount = messageCount
	w.lastExactTokens = exactTokens
}

func (w *contextWindow) recordAction(proposed modelprotocol.HostAction, raw json.RawMessage) {
	w.journal = append(w.journal, contextJournalEntry{EntityID: proposed.EntityID, Name: proposed.Name,
		Reason: boundedContextText(proposed.Reason, 160), Summary: summarizeContextAction(proposed.Name, proposed.Arguments, raw), Success: contextActionSucceeded(raw)})
}

func (w *contextWindow) compact(messages []provider.Message, budget policy.ContextBudget) ([]provider.Message, contextCompaction, error) {
	before := w.estimate(messages)
	if budget.CompactAtTokens <= 0 || before < budget.CompactAtTokens {
		return messages, contextCompaction{}, nil
	}
	if len(messages) <= len(w.base) {
		return messages, contextCompaction{}, nil
	}
	checkpoint := provider.Message{Role: "user", Content: w.checkpoint(messages[len(w.base):])}
	compacted := append(slices.Clone(w.base), checkpoint)

	// Preserve a small exact tail when it fits. It commonly contains the latest
	// host result or repair instruction, while the checkpoint covers older work.
	tail := messages[len(w.base):]
	var kept []provider.Message
	for index := len(tail) - 1; index >= 0 && len(kept) < 4; index-- {
		candidate := append(slices.Clone(compacted), append([]provider.Message{tail[index]}, kept...)...)
		if budget.TargetTokens > 0 && estimateProviderMessages(candidate) > budget.TargetTokens {
			break
		}
		kept = append([]provider.Message{tail[index]}, kept...)
	}
	compacted = append(compacted, kept...)
	after := estimateProviderMessages(compacted)
	if budget.LimitTokens > 0 && after > budget.LimitTokens {
		return nil, contextCompaction{}, fmt.Errorf("the public conversation and required instructions need about %d tokens, above Midgard's %d-token quality limit; start a new chat with a concise handoff", after, budget.LimitTokens)
	}
	w.lastRequestCount = 0
	w.lastExactTokens = 0
	return compacted, contextCompaction{BeforeTokens: before, AfterTokens: after, Removed: len(messages) - len(compacted)}, nil
}

func (w *contextWindow) checkpoint(history []provider.Message) string {
	var output strings.Builder
	output.WriteString("MIDGARD CONTEXT CHECKPOINT (server-authored)\n")
	output.WriteString("Older conversation turns, model protocol, and raw host observations were removed from working context. Canonical messages, events, action results, provider traces, artifacts, and Git state remain durable.\n")
	fmt.Fprintf(&output, "Current user outcome: %s\n", boundedContextText(w.objective, 1000))
	excerpts := contextConversationExcerpts(history, 12)
	if len(excerpts) > 0 {
		output.WriteString("Recent conversation excerpts (attributed, truncated):\n")
		for _, excerpt := range excerpts {
			fmt.Fprintf(&output, "- %s: %s\n", excerpt.Role, excerpt.Content)
		}
	}
	if len(w.journal) == 0 {
		output.WriteString("No completed host actions are recorded in this checkpoint.\n")
	} else {
		output.WriteString("Completed host actions:\n")
		start := max(0, len(w.journal)-80)
		if start > 0 {
			fmt.Fprintf(&output, "- %d older completed actions omitted from this working checkpoint; re-inspect canonical state if needed.\n", start)
		}
		loadedSkills := map[string]bool{}
		for _, entry := range w.journal[start:] {
			state := "failed"
			if entry.Success {
				state = "succeeded"
			}
			fmt.Fprintf(&output, "- %s %s %s — %s", entry.EntityID, entry.Name, state, entry.Summary)
			if entry.Reason != "" {
				fmt.Fprintf(&output, "; purpose: %s", entry.Reason)
			}
			output.WriteByte('\n')
			if entry.Name == "skill_read" {
				loadedSkills[entry.Summary] = true
			}
		}
		if len(loadedSkills) > 0 {
			output.WriteString("Skill contents may have been removed by compaction. Re-read a matching skill before relying on its detailed instructions.\n")
		}
	}
	output.WriteString("Use new unique entity IDs. Re-inspect files when exact content matters, use Git for current source state, and do not infer success from this checkpoint alone.\n")
	return boundedContextText(output.String(), 16<<10)
}

func contextConversationExcerpts(messages []provider.Message, limit int) []provider.Message {
	var excerpts []provider.Message
	for index := len(messages) - 1; index >= 0 && len(excerpts) < limit; index-- {
		message := messages[index]
		content := strings.TrimSpace(message.Content)
		if (message.Role != "user" && message.Role != "assistant") || content == "" ||
			strings.HasPrefix(content, "+ @") || strings.HasPrefix(content, "MIDGARD ") ||
			strings.HasPrefix(content, "No executable tool") {
			continue
		}
		excerpts = append([]provider.Message{{Role: message.Role, Content: boundedContextText(content, 500)}}, excerpts...)
	}
	return excerpts
}

func estimateProviderMessages(messages []provider.Message) int64 {
	var bytes int64
	for _, message := range messages {
		bytes += int64(len(message.Role) + len(message.Content) + 24)
		if message.ReplayState != nil {
			bytes += int64(len(message.ReplayState.Payload) + len(message.ReplayState.Adapter))
		}
		for _, call := range message.ToolCalls {
			bytes += int64(len(call.ID) + len(call.Name) + len(call.Arguments) + 24)
		}
	}
	// Code, JSON, and protocol punctuation tokenize more densely than prose.
	// Two bytes per token is intentionally conservative until the provider
	// supplies an exact count for the request.
	return (bytes + 1) / 2
}

func contextActionSucceeded(raw json.RawMessage) bool {
	var value struct {
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return value.ExitCode == 0 && value.Error == ""
}

func summarizeContextAction(name string, arguments, raw json.RawMessage) string {
	var args map[string]any
	_ = json.Unmarshal(arguments, &args)
	stringArg := func(key string) string {
		value, _ := args[key].(string)
		return boundedContextText(strings.TrimSpace(value), 180)
	}
	var result workspace.Output
	_ = json.Unmarshal(raw, &result)
	switch name {
	case "environment_describe":
		return "described the selected runtime environment without values"
	case "skill_search":
		return "searched available skills for " + stringArg("query")
	case "skill_read":
		summary := "skill " + stringArg("name")
		if resource := stringArg("resource"); resource != "" {
			summary += " resource " + resource
		}
		if query := stringArg("query"); query != "" {
			summary += " searched for " + query
		}
		return summary
	case "repo_search":
		summary := "searched repository for " + stringArg("query")
		if path := stringArg("path"); path != "" {
			summary += " under " + path
		}
		return fmt.Sprintf("%s (%d result bytes); search again if needed", summary, len(result.Stdout))
	case "file_inspect":
		summary := "read " + stringArg("path")
		if result.SHA256 != "" {
			summary += " at " + result.SHA256
		}
		return summary + "; content omitted, re-read if needed"
	case "file_replace":
		return "replaced " + stringArg("path") + "; Git owns current content"
	case "patch_apply":
		return "applied a bounded Git patch; patch body omitted"
	case "git_diff":
		return fmt.Sprintf("observed the Git diff (%d output bytes); re-run for current detail", len(result.Stdout))
	case "check_run":
		return fmt.Sprintf("check exited %d", result.ExitCode)
	case "shell":
		command := stringArg("command")
		if line, _, found := strings.Cut(command, "\n"); found {
			command = line + " …"
		}
		if result.JobID != "" {
			return fmt.Sprintf("started background job %s for command %q; poll for output", result.JobID, command)
		}
		return fmt.Sprintf("command %q exited %d; raw output omitted", command, result.ExitCode)
	case "shell_poll":
		return fmt.Sprintf("background job %s was %s; incremental output omitted", stringArg("job_id"), result.Status)
	case "shell_stop":
		return fmt.Sprintf("background job %s was stopped; final output omitted", stringArg("job_id"))
	default:
		return "result recorded; raw output omitted"
	}
}

func boundedContextText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
