package githubauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// LoopbackFlow is an ephemeral, authenticated OAuth callback receiver. Neither
// its state nor client secret is serialized or exposed to browser code.
type LoopbackFlow struct {
	AuthorizationURL string
	RedirectURI      string
	service          *ServiceClient
	capability       Capability
	state, secret    string
	listener         net.Listener
	server           *http.Server
	result           chan error
	once             sync.Once
}

func (s *ServiceClient) tokenEndpoint() string {
	if strings.TrimSpace(s.TokenEndpoint) != "" {
		return s.TokenEndpoint
	}
	return "https://github.com/login/oauth/access_token"
}
func (s *ServiceClient) authorizeEndpoint() string {
	if strings.TrimSpace(s.AuthorizeEndpoint) != "" {
		return s.AuthorizeEndpoint
	}
	return "https://github.com/login/oauth/authorize"
}
func clientSecretAccount(c Capability) string { return account(c) + ":client-secret" }

// SetClientSecret persists an OAuth App secret only in the OS SecretStore.
func (s *ServiceClient) SetClientSecret(c Capability, secret string) error {
	if !valid(c) {
		return errors.New("unknown GitHub capability")
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("refusing an empty GitHub OAuth client secret")
	}
	return s.Secrets.Set(Service, clientSecretAccount(c), secret)
}

// StartLoopback is the primary browser flow. The App must permit the generated
// 127.0.0.1 callback; callers can fall back to the existing device flow only
// where an App registration cannot allow a loopback redirect.
func (s *ServiceClient) StartLoopback(ctx context.Context, c Capability) (*LoopbackFlow, error) {
	id, err := s.clientID(c)
	if err != nil {
		return nil, err
	}
	secret, err := s.Secrets.Get(Service, clientSecretAccount(c))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("configure the %s GitHub App OAuth client secret in the system secret store before browser login", c)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("open loopback OAuth listener: %w", err)
	}
	state, err := newOAuthState()
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	redirect := "http://" + ln.Addr().String() + "/oauth/callback"
	q := url.Values{"client_id": {id}, "redirect_uri": {redirect}, "state": {state}}
	f := &LoopbackFlow{AuthorizationURL: s.authorizeEndpoint() + "?" + q.Encode(), RedirectURI: redirect, service: s, capability: c, state: state, secret: secret, listener: ln, result: make(chan error, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", f.callback)
	f.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = f.server.Serve(ln) }()
	go func() { <-ctx.Done(); f.finish(ctx.Err()) }()
	return f, nil
}
func newOAuthState() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (f *LoopbackFlow) callback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(f.state)) != 1 {
		http.Error(w, "OAuth state mismatch", 403)
		f.finish(errors.New("GitHub OAuth callback state mismatch"))
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		http.Error(w, "GitHub authorization was denied", 403)
		f.finish(fmt.Errorf("GitHub OAuth authorization failed: %s", e))
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "OAuth code missing", 400)
		f.finish(errors.New("GitHub OAuth callback has no code"))
		return
	}
	if e := f.exchange(r.Context(), code); e != nil {
		http.Error(w, "GitHub authorization failed", 502)
		f.finish(e)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><title>cmux-localreview connected</title><p>GitHub authorization is complete. You can close this tab.</p>"))
	f.finish(nil)
}
func (f *LoopbackFlow) exchange(ctx context.Context, code string) error {
	id, e := f.service.clientID(f.capability)
	if e != nil {
		return e
	}
	body, status, e := f.service.request(ctx, f.service.tokenEndpoint(), url.Values{"client_id": {id}, "client_secret": {f.secret}, "code": {code}, "redirect_uri": {f.RedirectURI}})
	if e != nil {
		return e
	}
	access := field(body, "access_token")
	if status < 200 || status > 299 || access == "" {
		return errors.New(message(body, "GitHub OAuth token exchange failed"))
	}
	t := Token{AccessToken: access, RefreshToken: field(body, "refresh_token"), ClientID: id}
	if secs := number(body["expires_in"], 0); secs > 0 {
		t.ExpiresAt = f.service.Now().Add(time.Duration(secs) * time.Second).UnixMilli()
	}
	login, e := f.service.viewer(ctx, access)
	if e != nil {
		return e
	}
	t.Login = login
	if e = f.service.write(f.capability, t); e != nil {
		return e
	}
	f.service.state[f.capability] = "succeeded"
	f.service.message[f.capability] = "Connected as @" + login + "."
	return nil
}
func (f *LoopbackFlow) finish(e error) {
	f.once.Do(func() { f.result <- e; go func() { _ = f.server.Close() }() })
}
func (f *LoopbackFlow) Wait(ctx context.Context) error {
	select {
	case e := <-f.result:
		return e
	case <-ctx.Done():
		f.Close()
		return ctx.Err()
	}
}
func (f *LoopbackFlow) Close() { f.finish(errors.New("GitHub OAuth login canceled")) }
