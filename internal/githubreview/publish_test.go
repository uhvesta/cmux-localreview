package githubreview

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/remotepr"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) Do(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, text string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(text)), Header: make(http.Header)}
}
func fixturePR() remotepr.PullRequest {
	return remotepr.PullRequest{URL: "https://github.com/acme/widget/pull/7", Number: 7, Repository: "acme/widget", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BaseSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", HeadRefName: "feature", BaseRefName: "main", State: "OPEN"}
}
func prJSON(head, state string) string {
	return `{"number":7,"html_url":"https://github.com/acme/widget/pull/7","title":"Review","state":"` + state + `","draft":false,"head":{"sha":"` + head + `","ref":"feature"},"base":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","ref":"main","repo":{"full_name":"acme/widget","clone_url":"https://github.com/acme/widget.git"}}}`
}

func TestPublishChecksHeadThenPublishesWithSeparateTokens(t *testing.T) {
	var publishBody string
	client := Client{HTTP: transport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			if r.Header.Get("Authorization") != "Bearer read-app" {
				t.Fatalf("read auth=%q", r.Header.Get("Authorization"))
			}
			return response(200, prJSON(fixturePR().HeadSHA, "open")), nil
		}
		if r.URL.Path != "/repos/acme/widget/pulls/7/reviews" || r.Header.Get("Authorization") != "Bearer write-app" {
			t.Fatalf("write request %s %q", r.URL, r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		publishBody = string(b)
		return response(200, `{"id":42}`), nil
	})}
	path, side, line := "src/main.go", "new", 6
	result, err := client.Publish(context.Background(), fixturePR(), RequestChanges, "Please address this.", []Feedback{{Body: "unsafe", Path: &path, Side: &side, Line: &line}, {Body: "also add tests"}}, "read-app", "write-app")
	if err != nil {
		t.Fatal(err)
	}
	if result.ReviewID != 42 || result.InlineComments != 1 || !strings.Contains(publishBody, `"event":"REQUEST_CHANGES"`) || !strings.Contains(publishBody, `"commit_id":"aaaaaaaa`) || !strings.Contains(publishBody, `"path":"src/main.go"`) {
		t.Fatalf("result=%+v body=%s", result, publishBody)
	}
}

func TestPublishRejectsStaleHeadBeforeAnyWrite(t *testing.T) {
	writes := 0
	client := Client{HTTP: transport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			writes++
			return response(200, `{}`), nil
		}
		return response(200, prJSON("cccccccccccccccccccccccccccccccccccccccc", "open")), nil
	})}
	_, err := client.Publish(context.Background(), fixturePR(), Approve, "", nil, "read", "write")
	if _, ok := err.(*StaleHeadError); !ok || writes != 0 {
		t.Fatalf("err=%v writes=%d", err, writes)
	}
}

func TestPublishRejectsUnsafeInlinePathBeforeWrite(t *testing.T) {
	writes := 0
	client := Client{HTTP: transport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			writes++
		}
		return response(200, prJSON(fixturePR().HeadSHA, "open")), nil
	})}
	bad, line := "../secret", 1
	_, err := client.Publish(context.Background(), fixturePR(), Comment, "", []Feedback{{Body: "no", Path: &bad, Line: &line}}, "read", "write")
	if err == nil || !strings.Contains(err.Error(), "repository-relative") || writes != 0 {
		t.Fatalf("err=%v writes=%d", err, writes)
	}
}
