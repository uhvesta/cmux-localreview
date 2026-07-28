package githubauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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

type invocation struct {
	name  string
	args  []string
	stdin []byte
}
type runner struct {
	calls       []invocation
	out, errOut []byte
	err         error
}

func (r *runner) Run(n string, a []string, in []byte) ([]byte, []byte, error) {
	r.calls = append(r.calls, invocation{n, append([]string{}, a...), append([]byte{}, in...)})
	return r.out, r.errOut, r.err
}
func TestOSSecretStoreCommandBoundaries(t *testing.T) {
	for _, tc := range []struct{ os, get, set string }{{"darwin", "security", "security"}, {"linux", "secret-tool", "secret-tool"}} {
		t.Run(tc.os, func(t *testing.T) {
			r := &runner{out: []byte("token\n")}
			s := &OSSecretStore{OS: tc.os, Runner: r}
			v, e := s.Get("svc", "acct")
			if e != nil || v != "token" {
				t.Fatalf("get %q %v", v, e)
			}
			if r.calls[0].name != tc.get {
				t.Fatal(r.calls)
			}
			if e = s.Set("svc", "acct", "token"); e != nil {
				t.Fatal(e)
			}
			if r.calls[1].name != tc.set {
				t.Fatal(r.calls)
			}
			if tc.os == "linux" && string(r.calls[1].stdin) != "token" {
				t.Fatalf("linux token must be stdin: %#v", r.calls[1])
			}
			if e = s.Delete("svc", "acct"); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestOSSecretStoreFailsClosed(t *testing.T) {
	s := &OSSecretStore{OS: "windows", Runner: &runner{}}
	if _, e := s.Get("s", "a"); e == nil {
		t.Fatal("unsupported OS must fail closed")
	}
	if e := s.Set("s", "a", "token"); e == nil {
		t.Fatal("unsupported OS must not write fallback")
	}
}

func TestLoopbackOAuthStateAndPKCEVerifierStayDaemonOnly(t *testing.T) {
	secrets := memSecrets{}
	cfg := memConfig{Read: "Iv1.loopback"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("client_secret") != "" || r.Form.Get("code_verifier") == "" || r.Form.Get("code") != "code" {
				t.Fatalf("bad exchange: %v", r.Form)
			}
			_, _ = w.Write([]byte(`{"access_token":"loop-token"}`))
		case "/user":
			if r.Header.Get("Authorization") != "Bearer loop-token" {
				t.Fatal("viewer did not use issued token")
			}
			_, _ = w.Write([]byte(`{"login":"octo"}`))
		default:
			t.Fatal(r.URL)
		}
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client := roundTrip(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		if clone.URL.Host == "api.github.com" {
			clone.URL.Scheme = baseURL.Scheme
			clone.URL.Host = baseURL.Host
		}
		return server.Client().Do(clone)
	})
	s := New(secrets, cfg, client, nil)
	s.TokenEndpoint = server.URL + "/token"
	s.AuthorizeEndpoint = "https://example.invalid/authorize"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, err := s.StartLoopback(ctx, Read)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(f.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("state") == "" || u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("code_challenge") == "" {
		t.Fatal("authorization URL must contain only the PKCE challenge, never the verifier")
	}
	bad, err := http.Get(f.RedirectURI + "?code=code&state=wrong")
	if err != nil {
		t.Fatal(err)
	}
	_ = bad.Body.Close()
	if err := f.Wait(context.Background()); err == nil {
		t.Fatal("state mismatch must fail")
	}
	// A new flow proves a valid callback persists a token without exposing it.
	f, err = s.StartLoopback(ctx, Read)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(f.AuthorizationURL)
	ok, err := http.Get(f.RedirectURI + "?code=code&state=" + url.QueryEscape(u.Query().Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	_ = ok.Body.Close()
	if err := f.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Token(context.Background(), Read); err != nil {
		t.Fatal(err)
	}
}

func TestAPIFacadeUsesExplicitFlowAndHidesCredentials(t *testing.T) {
	secrets := memSecrets{}
	cfg := memConfig{Read: "Iv1.readclient", Write: "Iv1.writeclient", Copilot: "Iv1.copilotclient"}
	s := New(secrets, cfg, roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/login/device/code" {
			return response(200, `{"device_code":"d","user_code":"C","verification_uri":"https://example/device"}`), nil
		}
		return response(500, `{}`), nil
	}), nil)
	api := API{Service: s}
	status, err := api.Status(context.Background())
	if err != nil || status.Provider != "github-oauth-pkce" || len(status.Capabilities) != 3 {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	result, flow, err := api.Start(context.Background(), StartRequest{Capability: Read})
	if err != nil || flow == nil || result.AuthorizationURL == "" || result.Flow != "loopback" {
		t.Fatalf("default result=%#v flow=%#v err=%v", result, flow, err)
	}
	flow.Close()
	result, flow, err = api.Start(context.Background(), StartRequest{Capability: Read, Flow: "device"})
	if err != nil || flow != nil || result.UserCode != "C" || result.Flow != "device" {
		t.Fatalf("device fallback result=%#v flow=%#v err=%v", result, flow, err)
	}
	if _, _, err := api.Start(context.Background(), StartRequest{Capability: Read, Flow: "bad"}); err == nil {
		t.Fatal("accepted implicit unsupported OAuth flow")
	}
	if err := api.Logout(context.Background(), Read); err != nil {
		t.Fatal(err)
	}
}

func TestFileConfigStorePersistsPublicClientIDsOnly(t *testing.T) {
	path := t.TempDir() + "/github-app.json"
	store := NewFileConfigStore(path)
	if err := store.SetClientID(Read, "Iv1.readonly"); err != nil {
		t.Fatal(err)
	}
	got, err := NewFileConfigStore(path).ClientID(Read)
	if err != nil || got != "Iv1.readonly" {
		t.Fatalf("ClientID = %q, %v", got, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "access_token") || !strings.Contains(string(contents), "readonly") {
		t.Fatalf("unexpected public config contents: %s", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}
}
