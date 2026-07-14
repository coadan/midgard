package state

import (
	"context"
	"database/sql"
)

type ForgeAccount struct {
	ID          string
	Kind        string
	BaseURL     string
	AuthProfile string
}

type ForgeRepo struct {
	RepoID        string
	ForgeID       string
	Owner         string
	Name          string
	DefaultBranch string
	URL           string
}

type TaskPRLink struct {
	ID         string
	TaskID     string
	RepoID     string
	GroupID    string
	ForgeID    string
	Number     int
	URL        string
	BaseBranch string
	HeadBranch string
	HeadSHA    string
	Source     string
}

type ForgePRSnapshot struct {
	ID                    string
	LinkID                string
	FetchedAt             string
	State                 string
	Draft                 bool
	Title                 string
	Author                string
	Labels                string
	MergeableState        string
	ReviewDecision        string
	CheckConclusion       string
	UnresolvedThreadCount int
	ReviewThreadsComplete bool
	ArtifactRef           string
	ChecksArtifactRef     string
	ThreadsArtifactRef    string
	ReviewsArtifactRef    string
	MergedAt              string
	ClosedAt              string
}

type ForgeCheckRun struct {
	ID          string
	LinkID      string
	Name        string
	Status      string
	Conclusion  string
	URL         string
	StartedAt   string
	CompletedAt string
}

type ForgeReviewThread struct {
	ID            string
	LinkID        string
	ThreadID      string
	Path          string
	Line          int
	Resolved      bool
	LastAuthor    string
	LastUpdatedAt string
	ArtifactRef   string
}

func (db *DB) UpsertForgeAccount(ctx context.Context, account ForgeAccount) error {
	_, err := db.fencedExecContext(ctx, `
INSERT INTO forge_accounts (id, kind, base_url, auth_profile)
VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  kind = excluded.kind,
  base_url = excluded.base_url,
  auth_profile = excluded.auth_profile`,
		account.ID, account.Kind, account.BaseURL, account.AuthProfile)
	return err
}

func (db *DB) ForgeAccount(ctx context.Context, id string) (ForgeAccount, error) {
	var account ForgeAccount
	err := db.conn.QueryRowContext(ctx, `
SELECT id, kind, base_url, auth_profile
FROM forge_accounts
WHERE id = ?`, id).Scan(&account.ID, &account.Kind, &account.BaseURL, &account.AuthProfile)
	return account, err
}

func (db *DB) ForgeAccounts(ctx context.Context) ([]ForgeAccount, error) {
	rows, err := db.conn.QueryContext(ctx, `
SELECT id, kind, base_url, auth_profile
FROM forge_accounts
ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []ForgeAccount
	for rows.Next() {
		var account ForgeAccount
		if err := rows.Scan(&account.ID, &account.Kind, &account.BaseURL, &account.AuthProfile); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (db *DB) UpsertForgeRepo(ctx context.Context, repo ForgeRepo) error {
	_, err := db.fencedExecContext(ctx, `
INSERT INTO forge_repos (repo_id, forge_id, owner, name, default_branch, url)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, forge_id) DO UPDATE SET
  owner = excluded.owner,
  name = excluded.name,
  default_branch = excluded.default_branch,
  url = excluded.url,
  updated_at = CURRENT_TIMESTAMP`,
		repo.RepoID, repo.ForgeID, repo.Owner, repo.Name, repo.DefaultBranch, repo.URL)
	return err
}

func (db *DB) ForgeRepo(ctx context.Context, repoID, forgeID string) (ForgeRepo, error) {
	var repo ForgeRepo
	err := db.conn.QueryRowContext(ctx, `
SELECT repo_id, forge_id, owner, name, default_branch, url
FROM forge_repos
WHERE repo_id = ? AND forge_id = ?`, repoID, forgeID).Scan(
		&repo.RepoID,
		&repo.ForgeID,
		&repo.Owner,
		&repo.Name,
		&repo.DefaultBranch,
		&repo.URL,
	)
	return repo, err
}

func (db *DB) FirstForgeRepoForRepo(ctx context.Context, repoID string) (ForgeRepo, error) {
	var repo ForgeRepo
	err := db.conn.QueryRowContext(ctx, `
SELECT repo_id, forge_id, owner, name, default_branch, url
FROM forge_repos
WHERE repo_id = ?
ORDER BY forge_id
LIMIT 1`, repoID).Scan(
		&repo.RepoID,
		&repo.ForgeID,
		&repo.Owner,
		&repo.Name,
		&repo.DefaultBranch,
		&repo.URL,
	)
	return repo, err
}

func (db *DB) ForgeReposForRepo(ctx context.Context, repoID string) ([]ForgeRepo, error) {
	rows, err := db.conn.QueryContext(ctx, `
SELECT repo_id, forge_id, owner, name, default_branch, url
FROM forge_repos
WHERE repo_id = ?
ORDER BY forge_id`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repos []ForgeRepo
	for rows.Next() {
		var repo ForgeRepo
		if err := rows.Scan(&repo.RepoID, &repo.ForgeID, &repo.Owner, &repo.Name, &repo.DefaultBranch, &repo.URL); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}

func (db *DB) UpsertTaskPRLink(ctx context.Context, link TaskPRLink) error {
	_, err := db.fencedExecContext(ctx, `
INSERT INTO task_pr_links (id, task_id, repo_id, group_id, forge_id, pr_number, url, base_branch, head_branch, head_sha, source)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(task_id, repo_id, forge_id, pr_number) DO UPDATE SET
  group_id = excluded.group_id,
  url = excluded.url,
  base_branch = excluded.base_branch,
  head_branch = excluded.head_branch,
  head_sha = excluded.head_sha,
  source = excluded.source,
  updated_at = CURRENT_TIMESTAMP`,
		link.ID,
		link.TaskID,
		link.RepoID,
		link.GroupID,
		link.ForgeID,
		link.Number,
		link.URL,
		link.BaseBranch,
		link.HeadBranch,
		link.HeadSHA,
		link.Source,
	)
	return err
}

func (db *DB) TaskPRLink(ctx context.Context, taskID, repoID string, number int) (TaskPRLink, error) {
	var link TaskPRLink
	err := db.conn.QueryRowContext(ctx, `
SELECT id, task_id, repo_id, group_id, forge_id, pr_number, url, base_branch, head_branch, head_sha, source
FROM task_pr_links
WHERE task_id = ? AND repo_id = ? AND pr_number = ?
ORDER BY forge_id
LIMIT 1`, taskID, repoID, number).Scan(
		&link.ID,
		&link.TaskID,
		&link.RepoID,
		&link.GroupID,
		&link.ForgeID,
		&link.Number,
		&link.URL,
		&link.BaseBranch,
		&link.HeadBranch,
		&link.HeadSHA,
		&link.Source,
	)
	return link, err
}

func (db *DB) TaskPRLinks(ctx context.Context, taskID string) ([]TaskPRLink, error) {
	rows, err := db.conn.QueryContext(ctx, `
SELECT id, task_id, repo_id, group_id, forge_id, pr_number, url, base_branch, head_branch, head_sha, source
FROM task_pr_links
WHERE task_id = ?
ORDER BY repo_id, forge_id, pr_number`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []TaskPRLink
	for rows.Next() {
		var link TaskPRLink
		if err := rows.Scan(
			&link.ID,
			&link.TaskID,
			&link.RepoID,
			&link.GroupID,
			&link.ForgeID,
			&link.Number,
			&link.URL,
			&link.BaseBranch,
			&link.HeadBranch,
			&link.HeadSHA,
			&link.Source,
		); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func (db *DB) DeleteTaskPRLink(ctx context.Context, linkID string) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := assertExecutionFences(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, statement := range []string{
		"DELETE FROM forge_check_runs WHERE link_id = ?",
		"DELETE FROM forge_review_threads WHERE link_id = ?",
		"DELETE FROM forge_pr_snapshots WHERE link_id = ?",
		"DELETE FROM task_pr_links WHERE id = ?",
	} {
		if _, err := tx.ExecContext(ctx, statement, linkID); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) InsertForgePRSnapshot(ctx context.Context, snapshot ForgePRSnapshot) error {
	_, err := db.fencedExecContext(ctx, `
INSERT INTO forge_pr_snapshots (
  id, link_id, fetched_at, state, draft, title, author, labels, mergeable_state,
  review_decision, check_conclusion, unresolved_thread_count, review_threads_complete, artifact_ref,
  checks_artifact_ref, threads_artifact_ref, reviews_artifact_ref, merged_at, closed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snapshot.ID,
		snapshot.LinkID,
		snapshot.FetchedAt,
		snapshot.State,
		boolInt(snapshot.Draft),
		snapshot.Title,
		snapshot.Author,
		snapshot.Labels,
		snapshot.MergeableState,
		snapshot.ReviewDecision,
		snapshot.CheckConclusion,
		snapshot.UnresolvedThreadCount,
		boolInt(snapshot.ReviewThreadsComplete),
		snapshot.ArtifactRef,
		snapshot.ChecksArtifactRef,
		snapshot.ThreadsArtifactRef,
		snapshot.ReviewsArtifactRef,
		snapshot.MergedAt,
		snapshot.ClosedAt,
	)
	return err
}

func (db *DB) LatestForgePRSnapshot(ctx context.Context, linkID string) (ForgePRSnapshot, error) {
	var snapshot ForgePRSnapshot
	var draft int
	var reviewThreadsComplete int
	err := db.conn.QueryRowContext(ctx, `
SELECT id, link_id, fetched_at, state, draft, title, author, labels, mergeable_state,
       review_decision, check_conclusion, unresolved_thread_count, review_threads_complete, artifact_ref,
       checks_artifact_ref, threads_artifact_ref, reviews_artifact_ref, merged_at, closed_at
FROM forge_pr_snapshots
WHERE link_id = ?
ORDER BY fetched_at DESC, id DESC
LIMIT 1`, linkID).Scan(
		&snapshot.ID,
		&snapshot.LinkID,
		&snapshot.FetchedAt,
		&snapshot.State,
		&draft,
		&snapshot.Title,
		&snapshot.Author,
		&snapshot.Labels,
		&snapshot.MergeableState,
		&snapshot.ReviewDecision,
		&snapshot.CheckConclusion,
		&snapshot.UnresolvedThreadCount,
		&reviewThreadsComplete,
		&snapshot.ArtifactRef,
		&snapshot.ChecksArtifactRef,
		&snapshot.ThreadsArtifactRef,
		&snapshot.ReviewsArtifactRef,
		&snapshot.MergedAt,
		&snapshot.ClosedAt,
	)
	snapshot.Draft = draft != 0
	snapshot.ReviewThreadsComplete = reviewThreadsComplete != 0
	return snapshot, err
}

func (db *DB) ReplaceForgeCheckRuns(ctx context.Context, linkID string, checks []ForgeCheckRun) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := assertExecutionFences(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM forge_check_runs WHERE link_id = ?`, linkID); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, check := range checks {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO forge_check_runs (id, link_id, name, status, conclusion, url, started_at, completed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			check.ID,
			linkID,
			check.Name,
			check.Status,
			check.Conclusion,
			check.URL,
			check.StartedAt,
			check.CompletedAt,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) ReplaceForgeReviewThreads(ctx context.Context, linkID string, threads []ForgeReviewThread) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := assertExecutionFences(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM forge_review_threads WHERE link_id = ?`, linkID); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, thread := range threads {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO forge_review_threads (
  id, link_id, thread_id, path, line, resolved, last_author, last_updated_at, artifact_ref
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			thread.ID,
			linkID,
			thread.ThreadID,
			thread.Path,
			thread.Line,
			boolInt(thread.Resolved),
			thread.LastAuthor,
			thread.LastUpdatedAt,
			thread.ArtifactRef,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) ForgeCheckRuns(ctx context.Context, linkID string) ([]ForgeCheckRun, error) {
	rows, err := db.conn.QueryContext(ctx, `
SELECT id, link_id, name, status, conclusion, url, started_at, completed_at
FROM forge_check_runs
WHERE link_id = ?
ORDER BY name`, linkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var checks []ForgeCheckRun
	for rows.Next() {
		var check ForgeCheckRun
		if err := rows.Scan(&check.ID, &check.LinkID, &check.Name, &check.Status, &check.Conclusion, &check.URL, &check.StartedAt, &check.CompletedAt); err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, rows.Err()
}

func (db *DB) ForgeReviewThreads(ctx context.Context, linkID string) ([]ForgeReviewThread, error) {
	rows, err := db.conn.QueryContext(ctx, `
SELECT id, link_id, thread_id, path, line, resolved, last_author, last_updated_at, artifact_ref
FROM forge_review_threads
WHERE link_id = ?
ORDER BY path, line, thread_id`, linkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var threads []ForgeReviewThread
	for rows.Next() {
		var thread ForgeReviewThread
		var resolved int
		if err := rows.Scan(
			&thread.ID,
			&thread.LinkID,
			&thread.ThreadID,
			&thread.Path,
			&thread.Line,
			&resolved,
			&thread.LastAuthor,
			&thread.LastUpdatedAt,
			&thread.ArtifactRef,
		); err != nil {
			return nil, err
		}
		thread.Resolved = resolved != 0
		threads = append(threads, thread)
	}
	return threads, rows.Err()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func IsNoRows(err error) bool {
	return err == sql.ErrNoRows
}
