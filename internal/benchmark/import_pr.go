package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"midgard/internal/gitrepo"
)

const defaultGitHubAPIBaseURL = "https://api.github.com"

type ImportPROptions struct {
	Repo          string
	PullNumber    int
	OutPath       string
	ReferencePath string
	CloneURL      string
	APIBaseURL    string
	Token         string
	HTTPClient    *http.Client
	WorkDir       string
}

type ImportPRResult struct {
	Manifest           Manifest
	ManifestPath       string
	ReferencePatchPath string
}

type githubRepoRef struct {
	Owner string
	Name  string
	Slug  string
}

type githubPull struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	HTMLURL        string `json:"html_url"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Base           struct {
		SHA  string `json:"sha"`
		Repo struct {
			FullName      string `json:"full_name"`
			CloneURL      string `json:"clone_url"`
			DefaultBranch string `json:"default_branch"`
		} `json:"repo"`
	} `json:"base"`
}

type githubPullFile struct {
	Filename string `json:"filename"`
}

func ImportPR(ctx context.Context, opts ImportPROptions) (ImportPRResult, error) {
	if strings.TrimSpace(opts.OutPath) == "" {
		return ImportPRResult{}, fmt.Errorf("out path is required")
	}
	if opts.PullNumber <= 0 {
		return ImportPRResult{}, fmt.Errorf("pull request number is required")
	}
	repo, err := parseGitHubRepo(opts.Repo)
	if err != nil {
		return ImportPRResult{}, err
	}
	apiBase := strings.TrimRight(opts.APIBaseURL, "/")
	if apiBase == "" {
		apiBase = defaultGitHubAPIBaseURL
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	pr, err := fetchGitHubPull(ctx, client, apiBase, opts.Token, repo, opts.PullNumber)
	if err != nil {
		return ImportPRResult{}, err
	}
	if !pr.Merged {
		return ImportPRResult{}, fmt.Errorf("github PR %s#%d is not merged", repo.Slug, opts.PullNumber)
	}
	if strings.TrimSpace(pr.Base.SHA) == "" || strings.TrimSpace(pr.MergeCommitSHA) == "" {
		return ImportPRResult{}, fmt.Errorf("github PR %s#%d is missing base or merged commit sha", repo.Slug, opts.PullNumber)
	}
	files, err := fetchGitHubPullFiles(ctx, client, apiBase, opts.Token, repo, opts.PullNumber)
	if err != nil {
		return ImportPRResult{}, err
	}
	cloneURL := firstNonEmpty(opts.CloneURL, pr.Base.Repo.CloneURL, githubHTTPSURL(repo))
	referencePath := opts.ReferencePath
	if referencePath == "" {
		referencePath = filepath.Join(filepath.Dir(opts.OutPath), "references", fmt.Sprintf("%s-pr-%d.patch", repoFileSlug(repo.Slug), opts.PullNumber))
	}
	if err := writeReferencePatch(ctx, cloneURL, pr.Base.SHA, pr.MergeCommitSHA, referencePath, opts.WorkDir); err != nil {
		return ImportPRResult{}, err
	}

	manifestID := fmt.Sprintf("%s-pr-%d", repoFileSlug(repo.Slug), opts.PullNumber)
	referenceRef, err := filepath.Rel(filepath.Dir(opts.OutPath), referencePath)
	if err != nil {
		referenceRef = referencePath
	}
	manifest := Manifest{
		ID:    manifestID,
		Title: fmt.Sprintf("%s PR #%d", repo.Slug, opts.PullNumber),
		Repos: []RepoSource{repoSourceForManifest("repo1", cloneURL, pr.Base.SHA)},
		Items: []Item{{
			ID:                   fmt.Sprintf("pr-%d", opts.PullNumber),
			Title:                pr.Title,
			Objective:            importPRObjective(repo.Slug, opts.PullNumber, pr.Title, pr.Body, files),
			TaskID:               benchmarkTaskID(manifestID, fmt.Sprintf("pr-%d", opts.PullNumber)),
			RepoIDs:              []string{"repo1"},
			ExpectedTouchedFiles: files,
			HiddenReferencePatch: filepath.ToSlash(referenceRef),
			HiddenReferencePRs: []ReferencePR{{
				Forge:        "github",
				Repo:         repo.Slug,
				Number:       opts.PullNumber,
				URL:          firstNonEmpty(pr.HTMLURL, githubPullURL(repo, opts.PullNumber)),
				MergedCommit: pr.MergeCommitSHA,
			}},
		}},
	}
	if err := writeManifest(opts.OutPath, manifest); err != nil {
		return ImportPRResult{}, err
	}
	return ImportPRResult{Manifest: manifest, ManifestPath: opts.OutPath, ReferencePatchPath: referencePath}, nil
}

func fetchGitHubPull(ctx context.Context, client *http.Client, apiBase, token string, repo githubRepoRef, number int) (githubPull, error) {
	var pr githubPull
	if err := getGitHubJSON(ctx, client, apiBase, token, fmt.Sprintf("/repos/%s/pulls/%d", repo.Slug, number), &pr); err != nil {
		return githubPull{}, err
	}
	return pr, nil
}

func fetchGitHubPullFiles(ctx context.Context, client *http.Client, apiBase, token string, repo githubRepoRef, number int) ([]string, error) {
	seen := map[string]bool{}
	var files []string
	for page := 1; ; page++ {
		var batch []githubPullFile
		path := fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", repo.Slug, number, page)
		if err := getGitHubJSON(ctx, client, apiBase, token, path, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, file := range batch {
			name := strings.TrimSpace(file.Filename)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			files = append(files, name)
		}
		if len(batch) < 100 {
			break
		}
	}
	slices.Sort(files)
	return files, nil
}

func getGitHubJSON(ctx context.Context, client *http.Client, apiBase, token, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "midgard")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("github api status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func writeReferencePatch(ctx context.Context, cloneURL, baseSHA, mergedSHA, referencePath, workDir string) error {
	if cloneURL == "" {
		return fmt.Errorf("clone url is required")
	}
	cleanup := func() {}
	if workDir == "" {
		tmp, err := os.MkdirTemp("", "midgard-import-pr-*")
		if err != nil {
			return err
		}
		workDir = tmp
		cleanup = func() { _ = os.RemoveAll(tmp) }
	}
	defer cleanup()
	clonePath := filepath.Join(workDir, "repo")
	if err := os.RemoveAll(clonePath); err != nil {
		return err
	}
	if err := gitrepo.Clone(ctx, cloneURL, clonePath); err != nil {
		return err
	}
	if err := ensureCommit(ctx, clonePath, baseSHA); err != nil {
		return fmt.Errorf("base commit %s: %w", baseSHA, err)
	}
	if err := ensureCommit(ctx, clonePath, mergedSHA); err != nil {
		return fmt.Errorf("merged commit %s: %w", mergedSHA, err)
	}
	patch, err := gitrepo.Run(ctx, clonePath, "diff", "--binary", baseSHA+".."+mergedSHA)
	if err != nil {
		return err
	}
	if strings.TrimSpace(patch) == "" {
		return fmt.Errorf("reference patch is empty for %s..%s", baseSHA, mergedSHA)
	}
	if err := os.MkdirAll(filepath.Dir(referencePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(referencePath, []byte(patch+"\n"), 0o644)
}

func ensureCommit(ctx context.Context, repoPath, sha string) error {
	if _, err := gitrepo.Run(ctx, repoPath, "cat-file", "-e", sha+"^{commit}"); err == nil {
		return nil
	}
	if _, err := gitrepo.Run(ctx, repoPath, "fetch", "origin", sha); err != nil {
		return err
	}
	_, err := gitrepo.Run(ctx, repoPath, "cat-file", "-e", sha+"^{commit}")
	return err
}

func writeManifest(path string, manifest Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func repoSourceForManifest(id, cloneURL, checkoutRef string) RepoSource {
	source := RepoSource{ID: id, CheckoutRef: checkoutRef}
	if looksLocalRepoSource(cloneURL) {
		source.Path = cloneURL
	} else {
		source.URL = cloneURL
	}
	return source
}

func looksLocalRepoSource(source string) bool {
	if filepath.IsAbs(source) || strings.HasPrefix(source, ".") {
		return true
	}
	parsed, err := url.Parse(source)
	return err != nil || parsed.Scheme == ""
}

func importPRObjective(repo string, number int, title, body string, files []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Recreate the source changes from merged GitHub PR %s#%d: %s.", repo, number, strings.TrimSpace(title))
	if len(files) > 0 {
		b.WriteString(" Keep source changes scoped to these touched files when possible: ")
		b.WriteString(strings.Join(files, ", "))
		b.WriteString(".")
	}
	body = strings.TrimSpace(body)
	if body != "" {
		const maxBody = 1200
		if len(body) > maxBody {
			body = body[:maxBody] + "\n[truncated]"
		}
		b.WriteString("\n\nPR body:\n")
		b.WriteString(body)
	}
	return b.String()
}

func parseGitHubRepo(value string) (githubRepoRef, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return githubRepoRef{}, fmt.Errorf("repo is required")
	}
	if strings.HasPrefix(value, "git@github.com:") {
		value = strings.TrimPrefix(value, "git@github.com:")
		value = strings.TrimSuffix(value, ".git")
		return githubRepoFromSlug(value)
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return githubRepoRef{}, err
		}
		if parsed.Host != "github.com" && parsed.Host != "www.github.com" {
			return githubRepoRef{}, fmt.Errorf("repo URL host %q is not github.com", parsed.Host)
		}
		value = strings.Trim(parsed.Path, "/")
		value = strings.TrimSuffix(value, ".git")
		return githubRepoFromSlug(value)
	}
	value = strings.TrimSuffix(value, ".git")
	return githubRepoFromSlug(value)
}

func githubRepoFromSlug(slug string) (githubRepoRef, error) {
	parts := strings.Split(strings.Trim(slug, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return githubRepoRef{}, fmt.Errorf("repo must be owner/name or a GitHub repo URL")
	}
	return githubRepoRef{Owner: parts[0], Name: parts[1], Slug: parts[0] + "/" + parts[1]}, nil
}

func githubHTTPSURL(repo githubRepoRef) string {
	return "https://github.com/" + repo.Slug + ".git"
}

func githubPullURL(repo githubRepoRef, number int) string {
	return "https://github.com/" + repo.Slug + "/pull/" + strconv.Itoa(number)
}

var fileSlugPattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func repoFileSlug(value string) string {
	value = fileSlugPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, ".-_")
	if value == "" {
		return "repo"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
