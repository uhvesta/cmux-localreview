// Package remotepr owns the safe, read-only GitHub pull-request materializer.
//
// A PR URL is resolved with a daemon-owned read credential and checked out in
// a cache-owned detached worktree.  It deliberately never uses gh, a Git
// credential helper, or a browser credential, and never writes inside a user
// repository.
package remotepr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type PullRequest struct {
	URL           string `json:"url"`
	Number        int    `json:"number"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	State         string `json:"state"`
	IsDraft       bool   `json:"isDraft"`
	Repository    string `json:"repository"`
	RepositoryURL string `json:"repositoryUrl"`
	HeadRefName   string `json:"headRefName"`
	HeadSHA       string `json:"headSha"`
	BaseRefName   string `json:"baseRefName"`
	BaseSHA       string `json:"baseSha"`
}

type Workspace struct {
	WorkspacePath string      `json:"workspacePath"`
	MirrorPath    string      `json:"mirrorPath"`
	WorktreePath  string      `json:"worktreePath"`
	PullRequest   PullRequest `json:"pullRequest"`
}

type Paths struct {
	MirrorPath   string `json:"mirrorPath"`
	WorktreePath string `json:"worktreePath"`
}

type Client struct {
	HTTP interface {
		Do(*http.Request) (*http.Response, error)
	}
	CacheRoot string
}

func (c Client) httpClient() interface {
	Do(*http.Request) (*http.Response, error)
} {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func target(raw string) (ownerRepo string, number int, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") {
		return "", 0, errors.New("expected an https://github.com/<owner>/<repo>/pull/<number> URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" {
		return "", 0, errors.New("expected an https://github.com/<owner>/<repo>/pull/<number> URL")
	}
	number, err = strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return "", 0, errors.New("expected an https://github.com/<owner>/<repo>/pull/<number> URL")
	}
	return parts[0] + "/" + parts[1], number, nil
}

type apiPullRequest struct {
	Number  int     `json:"number"`
	HTMLURL string  `json:"html_url"`
	Title   string  `json:"title"`
	Body    *string `json:"body"`
	State   string  `json:"state"`
	Draft   bool    `json:"draft"`
	Head    struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo *struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
			SSHURL   string `json:"ssh_url"`
			HTMLURL  string `json:"html_url"`
		} `json:"repo"`
	} `json:"base"`
}

func (c Client) Resolve(ctx context.Context, rawURL, token string) (PullRequest, error) {
	repository, number, err := target(rawURL)
	if err != nil {
		return PullRequest{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+"/pulls/"+strconv.Itoa(number), nil)
	if err != nil {
		return PullRequest{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return PullRequest{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return PullRequest{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PullRequest{}, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var value apiPullRequest
	if err := json.Unmarshal(body, &value); err != nil {
		return PullRequest{}, errors.New("GitHub API returned invalid PR JSON")
	}
	if value.Base.Repo == nil || value.Base.Repo.FullName == "" || value.HTMLURL == "" || value.Head.SHA == "" || value.Head.Ref == "" || value.Base.SHA == "" || value.Base.Ref == "" {
		return PullRequest{}, errors.New("GitHub pull request has incomplete repository or commit metadata")
	}
	cloneURL := value.Base.Repo.CloneURL
	if cloneURL == "" {
		cloneURL = value.Base.Repo.SSHURL
	}
	if cloneURL == "" {
		cloneURL = value.Base.Repo.HTMLURL
	}
	if cloneURL == "" {
		return PullRequest{}, errors.New("GitHub pull request has no clone URL")
	}
	bodyText := ""
	if value.Body != nil {
		bodyText = *value.Body
	}
	return PullRequest{URL: value.HTMLURL, Number: value.Number, Title: value.Title, Body: bodyText, State: value.State, IsDraft: value.Draft, Repository: value.Base.Repo.FullName, RepositoryURL: cloneURL, HeadRefName: value.Head.Ref, HeadSHA: value.Head.SHA, BaseRefName: value.Base.Ref, BaseSHA: value.Base.SHA}, nil
}

func (c Client) cacheRoot() (string, error) {
	if strings.TrimSpace(c.CacheRoot) == "" {
		return "", errors.New("remote PR cache root is required")
	}
	return filepath.Abs(c.CacheRoot)
}

func key(pr PullRequest) string {
	sum := sha256.Sum256([]byte("github.com:" + strings.ToLower(pr.Repository)))
	return hex.EncodeToString(sum[:])
}

func (c Client) Paths(pr PullRequest) (Paths, error) {
	root, err := c.cacheRoot()
	if err != nil {
		return Paths{}, err
	}
	cacheKey := key(pr)
	return Paths{MirrorPath: filepath.Join(root, "mirrors", cacheKey+".git"), WorktreePath: filepath.Join(root, "worktrees", cacheKey, fmt.Sprintf("pr-%d", pr.Number), pr.HeadSHA)}, nil
}

func git(ctx context.Context, dir string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func gitEnvironment(token string) []string {
	if token == "" {
		return []string{"GIT_TERMINAL_PROMPT=0"}
	}
	return []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Bearer " + token}
}

func (c Client) Prepare(ctx context.Context, pr PullRequest, token string) (Workspace, error) {
	paths, err := c.Paths(pr)
	if err != nil {
		return Workspace{}, err
	}
	if err := os.MkdirAll(filepath.Dir(paths.MirrorPath), 0o700); err != nil {
		return Workspace{}, err
	}
	env := gitEnvironment(token)
	if _, err := os.Stat(paths.MirrorPath); errors.Is(err, os.ErrNotExist) {
		if err := git(ctx, "", env, "clone", "--mirror", pr.RepositoryURL, paths.MirrorPath); err != nil {
			return Workspace{}, fmt.Errorf("unable to mirror %s: %w", pr.Repository, err)
		}
	} else if err != nil {
		return Workspace{}, err
	} else if err := git(ctx, "", env, "--git-dir", paths.MirrorPath, "remote", "set-url", "origin", pr.RepositoryURL); err != nil {
		return Workspace{}, err
	}
	if err := git(ctx, "", env, "--git-dir", paths.MirrorPath, "fetch", "--prune", "origin"); err != nil {
		return Workspace{}, fmt.Errorf("unable to update mirror: %w", err)
	}
	pullRef := fmt.Sprintf("+refs/pull/%d/head:refs/cmux-localreview/pulls/%d/head", pr.Number, pr.Number)
	if err := git(ctx, "", env, "--git-dir", paths.MirrorPath, "fetch", "origin", pullRef); err != nil {
		return Workspace{}, fmt.Errorf("unable to fetch PR #%d: %w", pr.Number, err)
	}
	if err := git(ctx, "", nil, "--git-dir", paths.MirrorPath, "cat-file", "-e", pr.HeadSHA+"^{commit}"); err != nil {
		return Workspace{}, fmt.Errorf("PR #%d head %s is unavailable: %w", pr.Number, pr.HeadSHA, err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.WorktreePath), 0o700); err != nil {
		return Workspace{}, err
	}
	if _, err := os.Stat(paths.WorktreePath); errors.Is(err, os.ErrNotExist) {
		if err := git(ctx, "", nil, "--git-dir", paths.MirrorPath, "worktree", "add", "--detach", paths.WorktreePath, pr.HeadSHA); err != nil {
			return Workspace{}, err
		}
	} else if err != nil {
		return Workspace{}, err
	} else if err := git(ctx, paths.WorktreePath, nil, "checkout", "--detach", "--force", pr.HeadSHA); err != nil {
		return Workspace{}, err
	}
	if err := git(ctx, paths.WorktreePath, nil, "update-ref", "refs/remotes/origin/"+pr.BaseRefName, pr.BaseSHA); err != nil {
		return Workspace{}, err
	}
	return Workspace{WorkspacePath: paths.WorktreePath, MirrorPath: paths.MirrorPath, WorktreePath: paths.WorktreePath, PullRequest: pr}, nil
}

func (c Client) Cleanup(ctx context.Context, pr PullRequest, removeMirror bool) (worktreeRemoved, mirrorRemoved bool, err error) {
	paths, err := c.Paths(pr)
	if err != nil {
		return false, false, err
	}
	if _, statErr := os.Stat(paths.WorktreePath); statErr == nil {
		if err := git(ctx, "", nil, "--git-dir", paths.MirrorPath, "worktree", "remove", "--force", paths.WorktreePath); err != nil {
			return false, false, err
		}
		worktreeRemoved = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, false, statErr
	}
	if removeMirror {
		if _, statErr := os.Stat(paths.MirrorPath); statErr == nil {
			if err := os.RemoveAll(paths.MirrorPath); err != nil {
				return worktreeRemoved, false, err
			}
			mirrorRemoved = true
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return worktreeRemoved, false, statErr
		}
	}
	return worktreeRemoved, mirrorRemoved, nil
}
