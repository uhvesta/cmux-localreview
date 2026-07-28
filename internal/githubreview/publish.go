// Package githubreview publishes explicit formal reviews through GitHub's
// Reviews API.  It is deliberately narrowly scoped: callers must provide the
// separate daemon-owned read and write GitHub OAuth App capabilities, and it never
// shells out to gh or consults user Git credentials.
package githubreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/uhvesta/cmux-localreview/internal/remotepr"
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Decision string

const (
	Approve        Decision = "approved"
	RequestChanges Decision = "changes_requested"
	Comment        Decision = "comment"
)

type Feedback struct {
	Body string
	Path *string
	Line *int
	Side *string
}

type Result struct {
	Method         string `json:"method"`
	ReviewID       int64  `json:"reviewId,omitempty"`
	InlineComments int    `json:"inlineComments"`
	HeadSHA        string `json:"headSha"`
}

// StaleHeadError is deliberately distinguishable so the HTTP layer can tell a
// reviewer to refresh rather than representing a changed PR as a transport
// failure. No write is attempted when it is returned.
type StaleHeadError struct{ Expected, Actual, Reason string }

func (e *StaleHeadError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	return fmt.Sprintf("GitHub pull request changed from %s to %s; refresh the queue before publishing a review", short(e.Expected), short(e.Actual))
}
func short(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

type Client struct{ HTTP HTTPDoer }

func (c Client) http() HTTPDoer {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func event(decision Decision) (string, error) {
	switch decision {
	case Approve:
		return "APPROVE", nil
	case RequestChanges:
		return "REQUEST_CHANGES", nil
	case Comment:
		return "COMMENT", nil
	default:
		return "", errors.New("decision must be approved, changes_requested, or comment")
	}
}

func reviewBody(summary string, feedback []Feedback) string {
	general := make([]string, 0, len(feedback))
	for _, item := range feedback {
		if item.Path == nil || item.Line == nil || *item.Line <= 0 {
			if text := strings.TrimSpace(item.Body); text != "" {
				general = append(general, "- "+text)
			}
		}
	}
	parts := []string{}
	if text := strings.TrimSpace(summary); text != "" {
		parts = append(parts, text)
	}
	if len(general) > 0 {
		parts = append(parts, "General feedback:\n"+strings.Join(general, "\n"))
	}
	if len(parts) == 0 {
		return "Reviewed with cmux-localreview."
	}
	return strings.Join(parts, "\n\n")
}

type inlineComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

func inline(feedback []Feedback) ([]inlineComment, error) {
	result := []inlineComment{}
	for _, entry := range feedback {
		if entry.Path == nil || entry.Line == nil || *entry.Line <= 0 {
			continue
		}
		file := strings.TrimSpace(*entry.Path)
		// Review paths are repository-relative. An absolute or climbing path
		// could otherwise cause the reviewer to believe an unrelated local file
		// was published to GitHub.
		if file == "" || strings.HasPrefix(file, "/") || strings.HasPrefix(file, "\\") || path.IsAbs(file) || file == ".." || strings.HasPrefix(file, "../") || strings.Contains(file, "\\") {
			return nil, fmt.Errorf("inline review path %q must be repository-relative", file)
		}
		side := "RIGHT"
		if entry.Side != nil && strings.EqualFold(strings.TrimSpace(*entry.Side), "old") {
			side = "LEFT"
		}
		body := strings.TrimSpace(entry.Body)
		if body == "" {
			return nil, errors.New("inline review comment body is required")
		}
		result = append(result, inlineComment{Path: file, Line: *entry.Line, Side: side, Body: body})
	}
	return result, nil
}

func (c Client) current(ctx context.Context, pr remotepr.PullRequest, readToken string) (remotepr.PullRequest, error) {
	return (remotepr.Client{HTTP: c.http()}).Resolve(ctx, pr.URL, readToken)
}

// Publish first re-resolves the PR through read-only authority and requires
// its head to equal the immutable snapshot. Only then does it use write
// authority for one Reviews API request.
func (c Client) Publish(ctx context.Context, pr remotepr.PullRequest, decision Decision, summary string, feedback []Feedback, readToken, writeToken string) (Result, error) {
	if strings.TrimSpace(readToken) == "" || strings.TrimSpace(writeToken) == "" {
		return Result{}, errors.New("dedicated GitHub OAuth App read and write capabilities are required")
	}
	apiEvent, err := event(decision)
	if err != nil {
		return Result{}, err
	}
	if pr.URL == "" || pr.Repository == "" || pr.Number <= 0 || pr.HeadSHA == "" {
		return Result{}, errors.New("remote review is missing immutable pull-request metadata")
	}
	current, err := c.current(ctx, pr, readToken)
	if err != nil {
		return Result{}, err
	}
	if !strings.EqualFold(current.State, "OPEN") {
		return Result{}, &StaleHeadError{Reason: fmt.Sprintf("GitHub pull request #%d is %s; refresh the queue before publishing a review", pr.Number, strings.ToLower(current.State))}
	}
	if current.HeadSHA != pr.HeadSHA {
		return Result{}, &StaleHeadError{Expected: pr.HeadSHA, Actual: current.HeadSHA}
	}
	comments, err := inline(feedback)
	if err != nil {
		return Result{}, err
	}
	payload := struct {
		CommitID string          `json:"commit_id"`
		Event    string          `json:"event"`
		Body     string          `json:"body"`
		Comments []inlineComment `json:"comments,omitempty"`
	}{CommitID: pr.HeadSHA, Event: apiEvent, Body: reviewBody(summary, feedback), Comments: comments}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/repos/"+pr.Repository+"/pulls/"+fmt.Sprint(pr.Number)+"/reviews", bytes.NewReader(b))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+writeToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("GitHub review API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var response struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(body, &response)
	return Result{Method: "github-review-api", ReviewID: response.ID, InlineComments: len(comments), HeadSHA: pr.HeadSHA}, nil
}
