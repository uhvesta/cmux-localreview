package remotepr

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestResolveFailsClosedAndUsesReadBearer(t *testing.T) {
	client := Client{CacheRoot: t.TempDir(), HTTP: transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.github.com/repos/acme/widget/pulls/7" || r.Header.Get("Authorization") != "Bearer read-token" {
			t.Fatalf("unexpected request %s auth=%q", r.URL, r.Header.Get("Authorization"))
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"number":7,"html_url":"https://github.com/acme/widget/pull/7","title":"Fix","body":null,"state":"open","draft":false,"head":{"sha":"abcdef","ref":"branch"},"base":{"sha":"123456","ref":"main","repo":{"full_name":"acme/widget","clone_url":"https://github.com/acme/widget.git"}}}`))}, nil
	})}
	pr, err := client.Resolve(context.Background(), "https://github.com/acme/widget/pull/7/files", "read-token")
	if err != nil || pr.HeadSHA != "abcdef" || pr.Body != "" || pr.Repository != "acme/widget" {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
	if _, err := client.Resolve(context.Background(), "https://evil.example/acme/widget/pull/7", "read-token"); err == nil {
		t.Fatal("non github.com URL unexpectedly accepted")
	}
}
