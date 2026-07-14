package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	githubAPIVersion = "2026-03-10"
	githubMaxPages   = 100
)

type GitHubClient struct {
	HTTPClient  *http.Client
	APIBaseURL  string
	GraphQLURL  string
	Token       string
	TokenSource string
	Now         func() time.Time
}

type GitHubAuthStatus struct {
	Authenticated bool
	Source        string
	Account       string
	Host          string
	RateLimit     int
	RateRemaining int
}

type GitHubFetchResult struct {
	Snapshot        SnapshotFile
	PullEvidence    []byte
	ChecksEvidence  []byte
	ThreadsEvidence []byte
	ReviewsEvidence []byte
}

func NewGitHubClient(baseURL, token, tokenSource string) *GitHubClient {
	apiBaseURL, graphqlURL := githubEndpoints(baseURL)
	return &GitHubClient{
		HTTPClient:  http.DefaultClient,
		APIBaseURL:  apiBaseURL,
		GraphQLURL:  graphqlURL,
		Token:       token,
		TokenSource: tokenSource,
		Now:         time.Now,
	}
}

func ResolveGitHubToken(ctx context.Context, baseURL, profile string) (token, source string, err error) {
	host := githubHost(baseURL)
	profile = strings.TrimSpace(profile)
	switch {
	case profile == "anonymous":
		return "", "anonymous", nil
	case strings.HasPrefix(profile, "env:"):
		name := strings.TrimSpace(strings.TrimPrefix(profile, "env:"))
		if name == "" || strings.ContainsAny(name, "= \t\r\n") {
			return "", "", fmt.Errorf("invalid GitHub env auth profile %q", profile)
		}
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			return "", "", fmt.Errorf("GitHub token environment variable %s is not set", name)
		}
		return value, "env:" + name, nil
	case profile == "env":
		return githubEnvToken()
	case profile == "gh" || strings.HasPrefix(profile, "gh:"):
		if configuredHost := strings.TrimSpace(strings.TrimPrefix(profile, "gh:")); configuredHost != "" && configuredHost != "gh" {
			host = configuredHost
		}
		return githubCLIToken(ctx, host)
	case profile != "":
		return "", "", fmt.Errorf("unsupported GitHub auth profile %q", profile)
	}
	if token, source, err := githubEnvToken(); err == nil {
		return token, source, nil
	}
	if token, source, err := githubCLIToken(ctx, host); err == nil {
		return token, source, nil
	}
	return "", "anonymous", nil
}

func githubEnvToken() (string, string, error) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token, "env:" + name, nil
		}
	}
	return "", "", fmt.Errorf("GH_TOKEN and GITHUB_TOKEN are not set")
}

func githubCLIToken(ctx context.Context, host string) (string, string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", "", fmt.Errorf("GitHub CLI is unavailable: %w", err)
	}
	cmd := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", host)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("GitHub CLI auth for %s: %w: %s", host, err, strings.TrimSpace(stderr.String()))
	}
	token := strings.TrimSpace(stdout.String())
	if token == "" {
		return "", "", fmt.Errorf("GitHub CLI returned an empty token for %s", host)
	}
	return token, "gh:" + host, nil
}

func (c *GitHubClient) AuthStatus(ctx context.Context) (GitHubAuthStatus, error) {
	status := GitHubAuthStatus{
		Authenticated: c.Token != "",
		Source:        firstNonBlank(c.TokenSource, "anonymous"),
		Host:          githubHost(c.APIBaseURL),
	}
	path := "/rate_limit"
	if c.Token != "" {
		path = "/user"
	}
	raw, headers, err := c.get(ctx, path)
	if err != nil {
		return status, err
	}
	if c.Token != "" {
		var user struct {
			Login string `json:"login"`
		}
		if err := json.Unmarshal(raw, &user); err != nil {
			return status, err
		}
		status.Account = user.Login
	}
	status.RateLimit, _ = strconv.Atoi(headers.Get("X-RateLimit-Limit"))
	status.RateRemaining, _ = strconv.Atoi(headers.Get("X-RateLimit-Remaining"))
	return status, nil
}

func (c *GitHubClient) FetchPullRequest(ctx context.Context, owner, name string, number int) (GitHubFetchResult, error) {
	if owner == "" || name == "" || number <= 0 {
		return GitHubFetchResult{}, fmt.Errorf("GitHub owner, repo, and PR number are required")
	}
	prefix := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	pullRaw, _, err := c.get(ctx, fmt.Sprintf("%s/pulls/%d", prefix, number))
	if err != nil {
		return GitHubFetchResult{}, err
	}
	var pull githubPull
	if err := json.Unmarshal(pullRaw, &pull); err != nil {
		return GitHubFetchResult{}, fmt.Errorf("decode GitHub PR: %w", err)
	}
	if pull.Head.SHA == "" {
		return GitHubFetchResult{}, fmt.Errorf("GitHub PR %s/%s#%d has no head SHA", owner, name, number)
	}
	checks, checksEvidence, err := c.fetchChecks(ctx, prefix, pull.Head.SHA)
	if err != nil {
		return GitHubFetchResult{}, err
	}
	reviewsEvidence, err := c.fetchReviews(ctx, prefix, number)
	if err != nil {
		return GitHubFetchResult{}, err
	}
	reviewDecision := "unknown"
	threadsComplete := false
	var threads []SnapshotThread
	threadsEvidence := []byte("{\"complete\":false,\"reason\":\"authentication-required\"}\n")
	if c.Token != "" {
		reviewDecision, threads, threadsEvidence, err = c.fetchReviewThreads(ctx, owner, name, number)
		if err != nil {
			return GitHubFetchResult{}, err
		}
		threadsComplete = true
	}
	labels := make([]string, 0, len(pull.Labels))
	for _, label := range pull.Labels {
		if label.Name != "" {
			labels = append(labels, label.Name)
		}
	}
	stateValue := normalizeGitHubPRState(pull)
	mergeableState := normalize(strings.ToLower(pull.MergeableState), "unknown")
	fetchedAt := c.now().UTC().Format(time.RFC3339Nano)
	snapshot := SnapshotFile{
		FetchedAt:             fetchedAt,
		State:                 stateValue,
		Draft:                 pull.Draft,
		Title:                 pull.Title,
		Author:                pull.User.Login,
		Labels:                labels,
		MergeableState:        mergeableState,
		ReviewDecision:        normalizeReviewDecision(reviewDecision),
		CheckConclusion:       summarizeChecks(checks),
		UnresolvedThreadCount: unresolvedThreadCount(threads),
		ReviewThreadsComplete: threadsComplete,
		URL:                   pull.HTMLURL,
		BaseBranch:            pull.Base.Ref,
		HeadBranch:            pull.Head.Ref,
		HeadSHA:               pull.Head.SHA,
		MergedAt:              nullString(pull.MergedAt),
		ClosedAt:              nullString(pull.ClosedAt),
		Checks:                checks,
		Threads:               threads,
	}
	return GitHubFetchResult{
		Snapshot:        snapshot,
		PullEvidence:    append(pullRaw, '\n'),
		ChecksEvidence:  checksEvidence,
		ThreadsEvidence: threadsEvidence,
		ReviewsEvidence: reviewsEvidence,
	}, nil
}

type githubPull struct {
	State          string  `json:"state"`
	Draft          bool    `json:"draft"`
	Title          string  `json:"title"`
	HTMLURL        string  `json:"html_url"`
	MergeableState string  `json:"mergeable_state"`
	MergedAt       *string `json:"merged_at"`
	ClosedAt       *string `json:"closed_at"`
	User           struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

type githubCheckRun struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	Conclusion  *string `json:"conclusion"`
	DetailsURL  string  `json:"details_url"`
	StartedAt   string  `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
}

type githubCheckRunsPage struct {
	TotalCount int              `json:"total_count"`
	CheckRuns  []githubCheckRun `json:"check_runs"`
}

type githubCombinedStatus struct {
	State    string `json:"state"`
	Statuses []struct {
		ID        int64  `json:"id"`
		State     string `json:"state"`
		Context   string `json:"context"`
		TargetURL string `json:"target_url"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	} `json:"statuses"`
}

func (c *GitHubClient) fetchChecks(ctx context.Context, prefix, sha string) ([]SnapshotCheck, []byte, error) {
	var pages []json.RawMessage
	var runs []githubCheckRun
	for page := 1; page <= githubMaxPages; page++ {
		raw, _, err := c.get(ctx, fmt.Sprintf("%s/commits/%s/check-runs?per_page=100&page=%d", prefix, url.PathEscape(sha), page))
		if err != nil {
			return nil, nil, err
		}
		pages = append(pages, append(json.RawMessage(nil), raw...))
		var decoded githubCheckRunsPage
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, nil, fmt.Errorf("decode GitHub check runs: %w", err)
		}
		runs = append(runs, decoded.CheckRuns...)
		if len(decoded.CheckRuns) < 100 || len(runs) >= decoded.TotalCount {
			break
		}
		if page == githubMaxPages {
			return nil, nil, fmt.Errorf("GitHub check run pagination exceeded %d pages", githubMaxPages)
		}
	}
	var statusPages []json.RawMessage
	var statuses []struct {
		ID        int64  `json:"id"`
		State     string `json:"state"`
		Context   string `json:"context"`
		TargetURL string `json:"target_url"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	for page := 1; page <= githubMaxPages; page++ {
		statusRaw, _, err := c.get(ctx, fmt.Sprintf("%s/commits/%s/status?per_page=100&page=%d", prefix, url.PathEscape(sha), page))
		if err != nil {
			return nil, nil, err
		}
		statusPages = append(statusPages, append(json.RawMessage(nil), statusRaw...))
		var combined githubCombinedStatus
		if err := json.Unmarshal(statusRaw, &combined); err != nil {
			return nil, nil, fmt.Errorf("decode GitHub combined status: %w", err)
		}
		statuses = append(statuses, combined.Statuses...)
		if len(combined.Statuses) < 100 {
			break
		}
		if page == githubMaxPages {
			return nil, nil, fmt.Errorf("GitHub combined status pagination exceeded %d pages", githubMaxPages)
		}
	}
	checks := make([]SnapshotCheck, 0, len(runs)+len(statuses))
	for _, run := range runs {
		checks = append(checks, SnapshotCheck{
			Name:        run.Name,
			Status:      normalizeCheckStatus(run.Status),
			Conclusion:  normalizeCheckConclusion(nullString(run.Conclusion)),
			URL:         run.DetailsURL,
			StartedAt:   run.StartedAt,
			CompletedAt: nullString(run.CompletedAt),
		})
	}
	for _, status := range statuses {
		checkStatus := "completed"
		if strings.EqualFold(status.State, "pending") {
			checkStatus = "pending"
		}
		checks = append(checks, SnapshotCheck{
			Name:        status.Context,
			Status:      checkStatus,
			Conclusion:  normalizeCheckConclusion(status.State),
			URL:         status.TargetURL,
			StartedAt:   status.CreatedAt,
			CompletedAt: status.UpdatedAt,
		})
	}
	evidence, err := json.MarshalIndent(map[string]any{
		"check_run_pages":       pages,
		"combined_status_pages": statusPages,
	}, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return checks, append(evidence, '\n'), nil
}

func (c *GitHubClient) fetchReviews(ctx context.Context, prefix string, number int) ([]byte, error) {
	var pages []json.RawMessage
	for page := 1; page <= githubMaxPages; page++ {
		raw, _, err := c.get(ctx, fmt.Sprintf("%s/pulls/%d/reviews?per_page=100&page=%d", prefix, number, page))
		if err != nil {
			return nil, err
		}
		pages = append(pages, append(json.RawMessage(nil), raw...))
		var reviews []json.RawMessage
		if err := json.Unmarshal(raw, &reviews); err != nil {
			return nil, fmt.Errorf("decode GitHub reviews: %w", err)
		}
		if len(reviews) < 100 {
			break
		}
		if page == githubMaxPages {
			return nil, fmt.Errorf("GitHub review pagination exceeded %d pages", githubMaxPages)
		}
	}
	evidence, err := json.MarshalIndent(map[string]any{"pages": pages}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(evidence, '\n'), nil
}

func (c *GitHubClient) fetchReviewThreads(ctx context.Context, owner, name string, number int) (string, []SnapshotThread, []byte, error) {
	const query = `query($owner:String!,$name:String!,$number:Int!,$cursor:String){
  repository(owner:$owner,name:$name){
    pullRequest(number:$number){
      reviewDecision
      reviewThreads(first:100,after:$cursor){
        nodes{id isResolved path line originalLine comments(last:1){nodes{author{login} updatedAt}}}
        pageInfo{hasNextPage endCursor}
      }
    }
  }
}`
	variables := map[string]any{"owner": owner, "name": name, "number": number, "cursor": nil}
	var pages []json.RawMessage
	var threads []SnapshotThread
	reviewDecision := "unknown"
	for page := 1; page <= githubMaxPages; page++ {
		raw, err := c.graphql(ctx, query, variables)
		if err != nil {
			return "", nil, nil, err
		}
		pages = append(pages, append(json.RawMessage(nil), raw...))
		var response struct {
			Data struct {
				Repository *struct {
					PullRequest *struct {
						ReviewDecision *string `json:"reviewDecision"`
						ReviewThreads  struct {
							Nodes []struct {
								ID           string `json:"id"`
								IsResolved   bool   `json:"isResolved"`
								Path         string `json:"path"`
								Line         *int   `json:"line"`
								OriginalLine *int   `json:"originalLine"`
								Comments     struct {
									Nodes []struct {
										Author *struct {
											Login string `json:"login"`
										} `json:"author"`
										UpdatedAt string `json:"updatedAt"`
									} `json:"nodes"`
								} `json:"comments"`
							} `json:"nodes"`
							PageInfo struct {
								HasNextPage bool    `json:"hasNextPage"`
								EndCursor   *string `json:"endCursor"`
							} `json:"pageInfo"`
						} `json:"reviewThreads"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return "", nil, nil, fmt.Errorf("decode GitHub review threads: %w", err)
		}
		if len(response.Errors) > 0 {
			return "", nil, nil, fmt.Errorf("GitHub GraphQL: %s", response.Errors[0].Message)
		}
		if response.Data.Repository == nil || response.Data.Repository.PullRequest == nil {
			return "", nil, nil, fmt.Errorf("GitHub PR %s/%s#%d was not found by GraphQL", owner, name, number)
		}
		pull := response.Data.Repository.PullRequest
		if pull.ReviewDecision != nil {
			reviewDecision = *pull.ReviewDecision
		}
		for _, node := range pull.ReviewThreads.Nodes {
			line := 0
			if node.Line != nil {
				line = *node.Line
			} else if node.OriginalLine != nil {
				line = *node.OriginalLine
			}
			thread := SnapshotThread{ID: node.ID, Path: node.Path, Line: line, Resolved: node.IsResolved}
			if len(node.Comments.Nodes) > 0 {
				comment := node.Comments.Nodes[len(node.Comments.Nodes)-1]
				if comment.Author != nil {
					thread.LastAuthor = comment.Author.Login
				}
				thread.LastUpdatedAt = comment.UpdatedAt
			}
			threads = append(threads, thread)
		}
		if !pull.ReviewThreads.PageInfo.HasNextPage {
			break
		}
		if page == githubMaxPages {
			return "", nil, nil, fmt.Errorf("GitHub review thread pagination exceeded %d pages", githubMaxPages)
		}
		if pull.ReviewThreads.PageInfo.EndCursor == nil {
			return "", nil, nil, fmt.Errorf("GitHub review thread pagination has no end cursor")
		}
		variables["cursor"] = *pull.ReviewThreads.PageInfo.EndCursor
	}
	evidence, err := json.MarshalIndent(map[string]any{"complete": true, "pages": pages}, "", "  ")
	if err != nil {
		return "", nil, nil, err
	}
	return reviewDecision, threads, append(evidence, '\n'), nil
}

func (c *GitHubClient) get(ctx context.Context, path string) ([]byte, http.Header, error) {
	return c.request(ctx, http.MethodGet, c.APIBaseURL+path, nil)
}

func (c *GitHubClient) graphql(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, err
	}
	raw, _, err := c.request(ctx, http.MethodPost, c.GraphQLURL, bytes.NewReader(body))
	return raw, err
}

func (c *GitHubClient) request(ctx context.Context, method, endpoint string, body io.Reader) ([]byte, http.Header, error) {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "midgard-forge-readonly")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, resp.Header, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(data))
		if len(message) > 2048 {
			message = message[:2048] + " [truncated]"
		}
		return nil, resp.Header, fmt.Errorf("GitHub API %s %s returned %d: %s", method, endpoint, resp.StatusCode, message)
	}
	return data, resp.Header, nil
}

func (c *GitHubClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func githubEndpoints(baseURL string) (string, string) {
	baseURL = strings.TrimRight(firstNonBlank(baseURL, defaultGitHubBaseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err == nil && strings.EqualFold(parsed.Hostname(), "github.com") {
		return "https://api.github.com", "https://api.github.com/graphql"
	}
	if strings.HasSuffix(baseURL, "/api/v3") {
		return baseURL, strings.TrimSuffix(baseURL, "/api/v3") + "/api/graphql"
	}
	return baseURL + "/api/v3", baseURL + "/api/graphql"
}

func githubHost(baseURL string) string {
	parsed, err := url.Parse(firstNonBlank(baseURL, defaultGitHubBaseURL))
	if err == nil && parsed.Hostname() != "" {
		if strings.EqualFold(parsed.Hostname(), "api.github.com") {
			return "github.com"
		}
		return parsed.Hostname()
	}
	return "github.com"
}

func normalizeGitHubPRState(pull githubPull) string {
	if pull.MergedAt != nil && *pull.MergedAt != "" {
		return "merged"
	}
	switch strings.ToLower(pull.State) {
	case "open":
		return "open"
	case "closed":
		return "closed"
	default:
		return "unknown"
	}
}

func normalizeReviewDecision(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "approved":
		return "approved"
	case "changes_requested", "changes-requested":
		return "changes_requested"
	case "review_required", "review-required":
		return "review_required"
	default:
		return "unknown"
	}
}

func normalizeCheckStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "completed":
		return "completed"
	case "queued", "in_progress", "waiting", "pending", "requested":
		return "pending"
	default:
		return "unknown"
	}
}

func normalizeCheckConclusion(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "success":
		return "success"
	case "failure", "error", "timed_out", "action_required", "startup_failure", "stale":
		return "failure"
	case "queued", "in_progress", "waiting", "pending", "requested", "":
		return "pending"
	case "skipped", "neutral":
		return "skipped"
	case "cancelled":
		return "cancelled"
	default:
		return "unknown"
	}
}

func summarizeChecks(checks []SnapshotCheck) string {
	if len(checks) == 0 {
		return "unknown"
	}
	result := "success"
	for _, check := range checks {
		switch check.Conclusion {
		case "failure":
			return "failure"
		case "pending":
			result = "pending"
		case "cancelled":
			if result != "pending" {
				result = "cancelled"
			}
		case "unknown":
			if result == "success" {
				result = "unknown"
			}
		}
	}
	return result
}

func unresolvedThreadCount(threads []SnapshotThread) int {
	count := 0
	for _, thread := range threads {
		if !thread.Resolved {
			count++
		}
	}
	return count
}

func nullString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
