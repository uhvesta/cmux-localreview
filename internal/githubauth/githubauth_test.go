package githubauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type memSecrets map[string]string

func (m memSecrets) Get(s, a string) (string, error) { return m[s+"/"+a], nil }
func (m memSecrets) Set(s, a, v string) error        { m[s+"/"+a] = v; return nil }
func (m memSecrets) Delete(s, a string) error        { delete(m, s+"/"+a); return nil }

type memConfig map[Capability]string

func (m memConfig) ClientID(c Capability) (string, error)    { return m[c], nil }
func (m memConfig) SetClientID(c Capability, v string) error { m[c] = v; return nil }

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) Do(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func TestDeviceFlowSeparatesCapabilitiesAndStoresOnlySecret(t *testing.T) {
	secrets := memSecrets{}
	cfg := memConfig{Read: "Iv1.readclient", Write: "Iv1.writeclient", Copilot: "Iv1.copilotclient"}
	stage := 0
	client := roundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/login/device/code":
			return response(200, `{"device_code":"device","user_code":"ABCD","verification_uri":"https://github.com/login/device","expires_in":900}`), nil
		case "/login/oauth/access_token":
			stage++
			if stage == 1 {
				return response(200, `{"error":"authorization_pending"}`), nil
			}
			return response(200, `{"access_token":"secret-token","refresh_token":"refresh","expires_in":3600}`), nil
		case "/user":
			if r.Header.Get("Authorization") != "Bearer secret-token" {
				t.Fatal("missing bearer")
			}
			return response(200, `{"login":"octo"}`), nil
		}
		t.Fatal(r.URL)
		return nil, nil
	})
	opened := ""
	s := New(secrets, cfg, client, func(url string) error { opened = url; return nil })
	s.Now = func() time.Time { return time.Unix(100, 0) }
	if _, err := s.Start(context.Background(), Read); err != nil {
		t.Fatal(err)
	}
	if opened == "" {
		t.Fatal("did not open browser")
	}
	waiting, err := s.Poll(context.Background(), Read)
	if err != nil || waiting.State != "waiting" {
		t.Fatalf("waiting %#v %v", waiting, err)
	}
	done, err := s.Poll(context.Background(), Read)
	if err != nil || !done.Authenticated || done.Login != "octo" {
		t.Fatalf("done %#v %v", done, err)
	}
	if len(secrets) != 1 || strings.Contains(strings.Join(mapValues(secrets), ""), "readclient") == false {
		t.Fatal("expected one app-owned secret record")
	}
	if _, err := s.Token(context.Background(), Write); err == nil {
		t.Fatal("write must not inherit read token")
	}
}
func TestClientIDChangeInvalidatesToken(t *testing.T) {
	secrets := memSecrets{}
	cfg := memConfig{Read: "Iv1.oldclient"}
	s := New(secrets, cfg, roundTrip(func(r *http.Request) (*http.Response, error) { return response(200, `{"login":"octo"}`), nil }), nil)
	if err := s.write(Read, Token{AccessToken: "x", ClientID: "Iv1.oldclient"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Configure(Read, "Iv1.newclient"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Token(context.Background(), Read); err == nil {
		t.Fatal("old token survived client-id replacement")
	}
}
func mapValues(m memSecrets) []string {
	out := []string{}
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
