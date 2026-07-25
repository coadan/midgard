package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	midgardforge "midgard/internal/forge"
	"midgard/internal/gitrepo"
	"midgard/internal/model"
	"midgard/internal/review"
	"midgard/internal/state"
	"midgard/internal/workbench"
)

const maxContextSnippetLines = 120
const maxContextDiffBytes = 16 << 10
const maxReportContextBytes = 6 << 10
const maxContextFileIndexEntries = 80
const maxObjectiveSnippetCandidateFiles = 64
const maxObjectiveSnippetFallbackFiles = 64

func contextPacket(ctx context.Context, status StatusResult, layout workbench.Layout) string {
	var b strings.Builder
	b.WriteString("task:")
	b.WriteString(status.Task.ID)
	b.WriteByte('\n')
	b.WriteString("objective:")
	b.WriteString(status.Task.Objective)
	b.WriteByte('\n')
	b.WriteString("artifact_dir:")
	b.WriteString(filepath.Join(layout.Artifacts, status.Task.ID))
	b.WriteByte('\n')
	for _, wt := range status.Worktrees {
		b.WriteString("repo:")
		b.WriteString(wt.RepoID)
		b.WriteString(" worktree:")
		b.WriteString(wt.Path)
		b.WriteString(" dirty:")
		b.WriteString(fmt.Sprintf("%t", wt.Dirty))
		b.WriteByte('\n')
	}
	b.WriteString("\navailable_tools:\n")
	b.WriteString(availableToolContext())
	guidance := repositoryGuidance(status.Worktrees)
	if guidance != "" {
		b.WriteString("\nrepository_guidance:\n")
		b.WriteString(guidance)
	}
	forgeDigest := forgeDigestContext(ctx, layout, status.Task.ID)
	if forgeDigest != "" {
		b.WriteString("\nforge_prs:\n")
		b.WriteString(forgeDigest)
	}
	fileIndex := objectiveFileIndex(status.Task.Objective, status.Worktrees)
	if fileIndex != "" {
		b.WriteString("\nrepo_file_index:\n")
		b.WriteString(fileIndex)
	}
	snippets := objectiveSnippets(status.Task.Objective, status.Worktrees)
	if snippets != "" {
		b.WriteString("\nsource_context:\n")
		b.WriteString(snippets)
	}
	diffs := worktreeDiffContext(ctx, status.Worktrees)
	if diffs != "" {
		b.WriteString("\nworktree_diff:\n")
		b.WriteString(diffs)
	}
	roleStatuses := latestRoleStatusContext(ctx, layout, status.Task.ID)
	if roleStatuses != "" {
		b.WriteString("\nlatest_role_statuses:\n")
		b.WriteString(roleStatuses)
	}
	reviewFindings := latestReviewFindingsContext(ctx, layout, status.Task.ID)
	if reviewFindings != "" {
		b.WriteString("\nreview_findings:\n")
		b.WriteString(reviewFindings)
	}
	feedback := latestFeedbackContext(ctx, layout, status.Task.ID)
	if feedback != "" {
		b.WriteString("\nlatest_feedback:\n")
		b.WriteString(feedback)
	}
	reports := latestReportContext(layout, status.Task.ID)
	if reports != "" {
		b.WriteString("\nlatest_role_reports:\n")
		b.WriteString(reports)
	}
	return b.String()
}

func forgeDigestContext(ctx context.Context, layout workbench.Layout, taskID string) string {
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return ""
	}
	defer db.Close()
	return midgardforge.Digest(ctx, layout.Root, db, taskID)
}

func latestRoleStatusContext(ctx context.Context, layout workbench.Layout, taskID string) string {
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return ""
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, taskID)
	if err != nil {
		return ""
	}
	latestByRole := map[string]roleStatus{}
	for _, event := range events {
		if event.Type != "role.completed" {
			continue
		}
		completed, ok := parseRoleCompletedEvent(event.Payload)
		if !ok {
			continue
		}
		latestByRole[completed.Role.String()] = completed
	}
	var b strings.Builder
	for _, role := range []string{"planner", "implementer", "reviewer"} {
		completed, ok := latestByRole[role]
		if !ok {
			continue
		}
		b.WriteString("role:")
		b.WriteString(role)
		b.WriteString(" status:")
		b.WriteString(completed.Status)
		if completed.Artifact != "" {
			b.WriteString(" artifact:")
			b.WriteString(completed.Artifact)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func latestFeedbackContext(ctx context.Context, layout workbench.Layout, taskID string) string {
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return ""
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, taskID)
	if err != nil {
		return ""
	}
	var latest feedbackStatus
	var ok bool
	for _, event := range events {
		if event.Type != "feedback.received" {
			continue
		}
		if parsed, parsedOK := parseFeedbackReceivedEvent(event.Payload); parsedOK {
			latest = parsed
			ok = true
		}
	}
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("status:")
	b.WriteString(latest.Status)
	if latest.Source != "" {
		b.WriteString(" source:")
		b.WriteString(latest.Source)
	}
	b.WriteByte('\n')
	message := latest.Message
	if len(message) > maxReportContextBytes {
		message = message[:maxReportContextBytes] + "\n[feedback truncated]\n"
	}
	b.WriteString(message)
	if !strings.HasSuffix(message, "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

func latestReviewFindingsContext(ctx context.Context, layout workbench.Layout, taskID string) string {
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return ""
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, taskID)
	if err != nil {
		return ""
	}
	var latest roleStatus
	var ok bool
	for _, event := range events {
		if event.Type != "role.completed" {
			continue
		}
		completed, parsed := parseRoleCompletedEvent(event.Payload)
		if !parsed || completed.Role != model.RoleReviewer {
			continue
		}
		latest = completed
		ok = true
	}
	if !ok || latest.Status != string(review.VerdictChangesRequested) {
		return ""
	}
	artifactPath := latest.Artifact
	if artifactPath == "" {
		artifactPath = "review.mdx"
	}
	data, err := os.ReadFile(filepath.Join(layout.Artifacts, taskID, artifactPath))
	if err != nil || len(data) == 0 {
		return ""
	}
	text := string(data)
	if len(text) > maxReportContextBytes {
		text = text[:maxReportContextBytes] + "\n[review findings truncated]\n"
	}
	var b strings.Builder
	b.WriteString("artifact:")
	b.WriteString(artifactPath)
	b.WriteByte('\n')
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

func objectiveFileIndex(objective string, worktrees []WorktreeStatus) string {
	matches := objectiveFileMatches(objective, worktrees, maxContextFileIndexEntries)
	if len(matches) == 0 {
		return ""
	}
	var b strings.Builder
	for _, match := range matches {
		b.WriteString("repo:")
		b.WriteString(match.RepoID)
		b.WriteString(" file:")
		b.WriteString(match.Path)
		b.WriteString(" score:")
		b.WriteString(fmt.Sprintf("%d", match.Score))
		b.WriteByte('\n')
	}
	return b.String()
}

type fileIndexMatch struct {
	RepoID string
	Root   string
	Path   string
	Score  int
}

func objectiveFileMatches(objective string, worktrees []WorktreeStatus, limit int) []fileIndexMatch {
	terms := objectiveSearchTerms(objective)
	if len(terms) == 0 {
		return nil
	}
	var matches []fileIndexMatch
	for _, wt := range worktrees {
		root := wt.Path
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if shouldSkipDir(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !looksTextPath(path) {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			score := fileIndexScore(rel, terms)
			if score == 0 {
				return nil
			}
			matches = append(matches, fileIndexMatch{RepoID: wt.RepoID, Root: root, Path: rel, Score: score})
			return nil
		})
	}
	sortFileIndexMatches(matches)
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func sortFileIndexMatches(matches []fileIndexMatch) {
	slices.SortFunc(matches, func(a, b fileIndexMatch) int {
		if a.Score != b.Score {
			return b.Score - a.Score
		}
		if a.RepoID != b.RepoID {
			return strings.Compare(a.RepoID, b.RepoID)
		}
		return strings.Compare(a.Path, b.Path)
	})
}

func objectiveSearchTerms(objective string) []string {
	seen := map[string]bool{}
	var terms []string
	for _, token := range objectiveTokens(objective) {
		for _, term := range strings.FieldsFunc(strings.ToLower(token), func(r rune) bool {
			return r == '_' || r == '-' || r == '.' || r == '/' || r == ':'
		}) {
			if len(term) < 4 || seen[term] || stopToken(term) {
				continue
			}
			seen[term] = true
			terms = append(terms, term)
		}
	}
	return terms
}

func fileIndexScore(path string, terms []string) int {
	normalized := strings.ToLower(strings.ReplaceAll(path, "_", "-"))
	base := strings.ToLower(strings.ReplaceAll(filepath.Base(path), "_", "-"))
	score := 0
	for _, term := range terms {
		term = strings.ReplaceAll(term, "_", "-")
		if strings.Contains(normalized, term) {
			score++
		}
		if strings.Contains(base, term) {
			score += 2
		}
	}
	if score == 0 {
		return 0
	}
	switch filepath.Ext(path) {
	case ".clj", ".cljc", ".cljs", ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs":
		score++
	case ".md", ".txt", ".rst":
		score--
	}
	return score
}

func latestReportContext(layout workbench.Layout, taskID string) string {
	root := filepath.Join(layout.Artifacts, taskID)
	var b strings.Builder
	for _, path := range []string{"plan.mdx", "implementation.mdx", "review.mdx"} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || len(data) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("artifact:")
		b.WriteString(path)
		b.WriteByte('\n')
		text := string(data)
		if len(text) > maxReportContextBytes {
			text = text[:maxReportContextBytes] + "\n[report truncated]\n"
		}
		b.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func worktreeDiffContext(ctx context.Context, worktrees []WorktreeStatus) string {
	var b strings.Builder
	for _, wt := range worktrees {
		if !wt.Dirty {
			continue
		}
		diff, err := gitrepo.Diff(ctx, wt.Path)
		if err != nil || strings.TrimSpace(diff) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("repo:")
		b.WriteString(wt.RepoID)
		b.WriteByte('\n')
		if len(diff) > maxContextDiffBytes {
			diff = diff[:maxContextDiffBytes] + "\n[diff truncated]\n"
		}
		b.WriteString(diff)
		if !strings.HasSuffix(diff, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func objectiveSnippets(objective string, worktrees []WorktreeStatus) string {
	tokens := objectiveTokens(objective)
	if len(tokens) == 0 {
		return ""
	}
	searchTerms := objectiveSearchTerms(objective)
	candidates := objectiveFileMatches(objective, worktrees, maxObjectiveSnippetCandidateFiles)
	fileMatches := snippetFileMatches(candidates, tokens, searchTerms)
	if len(fileMatches) == 0 {
		fileMatches = snippetFileMatches(objectiveFallbackSnippetFiles(worktrees, maxObjectiveSnippetFallbackFiles), tokens, searchTerms)
	}
	if len(fileMatches) == 0 {
		return ""
	}
	slices.SortFunc(fileMatches, func(a, b snippetFileMatch) int {
		if a.Score != b.Score {
			return b.Score - a.Score
		}
		if a.RepoID != b.RepoID {
			return strings.Compare(a.RepoID, b.RepoID)
		}
		return strings.Compare(a.Path, b.Path)
	})
	var b strings.Builder
	linesWritten := 0
	for _, match := range fileMatches {
		if linesWritten >= maxContextSnippetLines {
			break
		}
		b.WriteString("repo:")
		b.WriteString(match.RepoID)
		b.WriteString(" file:")
		b.WriteString(match.Path)
		b.WriteString(" score:")
		b.WriteString(fmt.Sprintf("%d", match.Score))
		b.WriteByte('\n')
		for _, line := range match.Lines {
			if linesWritten >= maxContextSnippetLines {
				break
			}
			b.WriteString(line)
			b.WriteByte('\n')
			linesWritten++
		}
	}
	return b.String()
}

func objectiveFallbackSnippetFiles(worktrees []WorktreeStatus, limit int) []fileIndexMatch {
	if limit <= 0 {
		return nil
	}
	var matches []fileIndexMatch
	for _, wt := range worktrees {
		root := wt.Path
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if shouldSkipDir(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !looksTextPath(path) {
				return nil
			}
			info, err := entry.Info()
			if err != nil || info.Size() > 1<<20 {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			matches = append(matches, fileIndexMatch{RepoID: wt.RepoID, Root: root, Path: filepath.ToSlash(rel)})
			if len(matches) >= limit {
				return errStopWalk
			}
			return nil
		})
		if len(matches) >= limit {
			break
		}
	}
	return matches
}

func snippetFileMatches(candidates []fileIndexMatch, tokens, searchTerms []string) []snippetFileMatch {
	var fileMatches []snippetFileMatch
	for _, candidate := range candidates {
		path := filepath.Join(candidate.Root, filepath.FromSlash(candidate.Path))
		info, err := os.Stat(path)
		if err != nil || info.Size() > 1<<20 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || !looksText(data) {
			continue
		}
		matches := matchingLines(string(data), tokens)
		if len(matches) == 0 {
			continue
		}
		fileMatches = append(fileMatches, snippetFileMatch{
			RepoID: candidate.RepoID,
			Path:   candidate.Path,
			Score:  contentMatchScore(candidate.Path, string(data), tokens, searchTerms),
			Lines:  matches,
		})
	}
	return fileMatches
}

var errStopWalk = filepath.SkipAll

type snippetFileMatch struct {
	RepoID string
	Path   string
	Score  int
	Lines  []string
}

func contentMatchScore(path, data string, tokens, searchTerms []string) int {
	lower := strings.ToLower(data)
	score := fileIndexScore(path, searchTerms)
	seen := map[string]bool{}
	for _, token := range tokens {
		for _, candidate := range tokenCandidates(token) {
			if candidate == "" || seen[candidate] {
				continue
			}
			seen[candidate] = true
			if strings.Contains(lower, candidate) {
				score += 3
			}
		}
	}
	return score
}

var tokenPattern = regexp.MustCompile("[A-Za-z][A-Za-z0-9_./-]{4,}")

func objectiveTokens(objective string) []string {
	seen := map[string]bool{}
	var tokens []string
	for _, token := range tokenPattern.FindAllString(objective, -1) {
		token = strings.Trim(token, "`'\".,:;()[]{}")
		if len(token) < 5 {
			continue
		}
		lower := strings.ToLower(token)
		if seen[lower] || stopToken(lower) {
			continue
		}
		seen[lower] = true
		tokens = append(tokens, token)
	}
	return tokens
}

func stopToken(token string) bool {
	return slices.Contains([]string{
		"change",
		"documentation",
		"wording",
		"behavior",
		"unchanged",
		"awkward",
		"sentence",
		"describes",
		"implementing",
	}, token)
}

func matchingLines(data string, tokens []string) []string {
	lines := contentLines(data)
	matched := map[int]bool{}
	for i, line := range lines {
		lower := strings.ToLower(line)
		for _, token := range tokens {
			if lineMatchesToken(lower, token) {
				for n := max(0, i-2); n <= min(len(lines)-1, i+2); n++ {
					matched[n] = true
				}
				break
			}
		}
	}
	indexes := make([]int, 0, len(matched))
	for index := range matched {
		indexes = append(indexes, index)
	}
	slices.Sort(indexes)
	out := make([]string, 0, len(indexes))
	for _, index := range indexes {
		out = append(out, fmt.Sprintf("%d:%s", index+1, lines[index]))
	}
	return out
}

func lineMatchesToken(lowerLine, token string) bool {
	for _, candidate := range tokenCandidates(token) {
		if candidate != "" && strings.Contains(lowerLine, candidate) {
			return true
		}
	}
	return false
}

func tokenCandidates(token string) []string {
	base := strings.ToLower(strings.Trim(token, "`'\".,:;()[]{}"))
	if base == "" {
		return nil
	}
	seen := map[string]bool{}
	var candidates []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		candidates = append(candidates, value)
	}
	add(base)
	add(strings.ReplaceAll(base, "_", "-"))
	add(strings.ReplaceAll(base, "-", "_"))
	for _, part := range strings.FieldsFunc(base, func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == '/'
	}) {
		if len(part) >= 5 && !stopToken(part) {
			add(part)
		}
	}
	return candidates
}

func contentLines(data string) []string {
	lines := strings.Split(data, "\n")
	if len(lines) > 0 && strings.HasSuffix(data, "\n") && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func shouldSkipDir(name string) bool {
	return slices.Contains([]string{".git", ".midgard", "node_modules", "vendor", ".venv", "dist"}, name)
}

func looksTextPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".clj", ".cljc", ".cljs", ".css", ".edn", ".go", ".html", ".java", ".js", ".json", ".jsx", ".kt", ".md", ".py", ".rs", ".rst", ".sql", ".toml", ".ts", ".tsx", ".txt", ".yaml", ".yml":
		return true
	default:
		return filepath.Base(path) == "README" || filepath.Base(path) == "LICENSE"
	}
}

func looksText(data []byte) bool {
	return !strings.ContainsRune(string(data), '\x00')
}
