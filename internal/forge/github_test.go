package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGitHubClientFetchesReadOnlyPullEvidence(t *testing.T) {
	var graphQLCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-GitHub-Api-Version") != githubAPIVersion {
			t.Errorf("api version = %q", r.Header.Get("X-GitHub-Api-Version"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls/42":
			_, _ = w.Write([]byte(`{
  "state":"open","draft":false,"title":"Fix widgets","html_url":"https://github.test/acme/widgets/pull/42",
  "mergeable_state":"clean","merged_at":null,"closed_at":null,"user":{"login":"alice"},
  "labels":[{"name":"bug"}],"base":{"ref":"main"},"head":{"ref":"fix-widgets","sha":"abcdef1234567890"}
}`))
		case "/repos/acme/widgets/commits/abcdef1234567890/check-runs":
			_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"id":1,"name":"unit","status":"completed","conclusion":"success","details_url":"https://ci.test/unit","started_at":"2026-07-10T00:00:00Z","completed_at":"2026-07-10T00:01:00Z"}]}`))
		case "/repos/acme/widgets/commits/abcdef1234567890/status":
			_, _ = w.Write([]byte(`{"state":"failure","statuses":[{"id":2,"state":"failure","context":"lint","target_url":"https://ci.test/lint","created_at":"2026-07-10T00:00:00Z","updated_at":"2026-07-10T00:01:00Z"}]}`))
		case "/repos/acme/widgets/pulls/42/reviews":
			_, _ = w.Write([]byte(`[ {"id":7,"state":"CHANGES_REQUESTED","user":{"login":"bob"}} ]`))
		case "/graphql":
			graphQLCalls++
			var request struct {
				Variables map[string]any `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if graphQLCalls == 1 {
				if request.Variables["cursor"] != nil {
					t.Errorf("first cursor = %#v", request.Variables["cursor"])
				}
				_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewDecision":"CHANGES_REQUESTED","reviewThreads":{"nodes":[{"id":"thread-1","isResolved":false,"path":"tags.go","line":7,"originalLine":7,"comments":{"nodes":[{"author":{"login":"bob"},"updatedAt":"2026-07-10T00:02:00Z"}]}}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"}}}}}}`))
				return
			}
			if request.Variables["cursor"] != "cursor-1" {
				t.Errorf("second cursor = %#v", request.Variables["cursor"])
			}
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewDecision":"CHANGES_REQUESTED","reviewThreads":{"nodes":[{"id":"thread-2","isResolved":true,"path":"tags_test.go","line":12,"originalLine":12,"comments":{"nodes":[]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &GitHubClient{
		HTTPClient:  server.Client(),
		APIBaseURL:  server.URL,
		GraphQLURL:  server.URL + "/graphql",
		Token:       "test-token",
		TokenSource: "test",
		Now: func() time.Time {
			return time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC)
		},
	}
	result, err := client.FetchPullRequest(context.Background(), "acme", "widgets", 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.State != "open" || result.Snapshot.HeadSHA != "abcdef1234567890" ||
		result.Snapshot.ReviewDecision != "changes_requested" || result.Snapshot.CheckConclusion != "failure" ||
		result.Snapshot.UnresolvedThreadCount != 1 || !result.Snapshot.ReviewThreadsComplete {
		t.Fatalf("snapshot = %#v", result.Snapshot)
	}
	if len(result.Snapshot.Checks) != 2 || len(result.Snapshot.Threads) != 2 || graphQLCalls != 2 {
		t.Fatalf("checks=%d threads=%d graphql=%d", len(result.Snapshot.Checks), len(result.Snapshot.Threads), graphQLCalls)
	}
	for name, data := range map[string][]byte{
		"pull": result.PullEvidence, "checks": result.ChecksEvidence,
		"threads": result.ThreadsEvidence, "reviews": result.ReviewsEvidence,
	} {
		if len(data) == 0 || !json.Valid(data) {
			t.Fatalf("%s evidence is not JSON: %q", name, data)
		}
	}
}

func TestGitHubAuthStatusAndAnonymousThreadHonesty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"login":"midgard-test"}`))
		case "/repos/acme/widgets/pulls/1":
			_, _ = w.Write([]byte(`{"state":"closed","draft":false,"title":"Done","html_url":"https://github.test/acme/widgets/pull/1","mergeable_state":"unknown","merged_at":"2026-07-10T00:00:00Z","closed_at":"2026-07-10T00:00:00Z","user":{"login":"alice"},"labels":[],"base":{"ref":"main"},"head":{"ref":"done","sha":"abc"}}`))
		case "/repos/acme/widgets/commits/abc/check-runs":
			_, _ = w.Write([]byte(`{"total_count":0,"check_runs":[]}`))
		case "/repos/acme/widgets/commits/abc/status":
			_, _ = w.Write([]byte(`{"state":"pending","statuses":[]}`))
		case "/repos/acme/widgets/pulls/1/reviews":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authenticated := &GitHubClient{HTTPClient: server.Client(), APIBaseURL: server.URL, Token: "token", TokenSource: "env:GH_TOKEN"}
	status, err := authenticated.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Authenticated || status.Account != "midgard-test" || status.RateRemaining != 4999 || strings.Contains(status.Source, "token") {
		t.Fatalf("auth status = %#v", status)
	}
	anonymous := &GitHubClient{HTTPClient: server.Client(), APIBaseURL: server.URL, GraphQLURL: server.URL + "/graphql", Now: time.Now}
	result, err := anonymous.FetchPullRequest(context.Background(), "acme", "widgets", 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.State != "merged" || result.Snapshot.ReviewThreadsComplete || result.Snapshot.UnresolvedThreadCount != 0 || !strings.Contains(string(result.ThreadsEvidence), "authentication-required") {
		t.Fatalf("anonymous snapshot = %#v evidence=%s", result.Snapshot, result.ThreadsEvidence)
	}
}

func TestResolveGitHubTokenFromNamedEnvironment(t *testing.T) {
	t.Setenv("MIDGARD_TEST_GITHUB_TOKEN", "secret-value")
	token, source, err := ResolveGitHubToken(context.Background(), "https://github.com", "env:MIDGARD_TEST_GITHUB_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if token != "secret-value" || source != "env:MIDGARD_TEST_GITHUB_TOKEN" {
		t.Fatalf("token/source = %q/%q", token, source)
	}
}

func TestGitHubClientPaginatesCombinedStatuses(t *testing.T) {
	statusCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/commits/abc/check-runs":
			_, _ = w.Write([]byte(`{"total_count":0,"check_runs":[]}`))
		case "/repos/acme/widgets/commits/abc/status":
			statusCalls++
			if r.URL.Query().Get("page") == "1" {
				statuses := make([]map[string]any, 100)
				for i := range statuses {
					statuses[i] = map[string]any{"id": i + 1, "state": "success", "context": fmt.Sprintf("check-%d", i+1)}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"state": "success", "statuses": statuses})
				return
			}
			_, _ = w.Write([]byte(`{"state":"failure","statuses":[{"id":101,"state":"failure","context":"last-check"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &GitHubClient{HTTPClient: server.Client(), APIBaseURL: server.URL}
	checks, evidence, err := client.fetchChecks(context.Background(), "/repos/acme/widgets", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 101 || checks[100].Conclusion != "failure" || statusCalls != 2 || !json.Valid(evidence) {
		t.Fatalf("checks=%d last=%#v calls=%d evidence=%q", len(checks), checks[100], statusCalls, evidence)
	}
	if host := githubHost("https://api.github.com"); host != "github.com" {
		t.Fatalf("GitHub API host = %q", host)
	}
}
