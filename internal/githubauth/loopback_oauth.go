package githubauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
// its state nor PKCE verifier is serialized or exposed to browser code.
type LoopbackFlow struct {
	AuthorizationURL string
	RedirectURI      string
	service          *ServiceClient
	capability       Capability
	state, verifier  string
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

// StartLoopback is the primary browser flow. GitHub OAuth App registrations
// need a pre-registered redirect URI, so the callback is deliberately stable
// rather than an ephemeral :0 port. The operator must register exactly
// http://127.0.0.1:8787/oauth/callback; use the device fallback only for a
// headless or SSH-only machine.
func (s *ServiceClient) StartLoopback(ctx context.Context, c Capability) (*LoopbackFlow, error) {
	id, err := s.clientID(c)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:8787")
	if err != nil {
		return nil, fmt.Errorf("open registered loopback OAuth listener on 127.0.0.1:8787 (or use device flow): %w", err)
	}
	state, err := newOAuthState()
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	verifier, err := newOAuthState()
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	redirect := "http://127.0.0.1:8787/oauth/callback"
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {id}, "redirect_uri": {redirect}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	if scopes := requestedScopeValue(c); scopes != "" {
		q.Set("scope", scopes)
	}
	f := &LoopbackFlow{AuthorizationURL: s.authorizeEndpoint() + "?" + q.Encode(), RedirectURI: redirect, service: s, capability: c, state: state, verifier: verifier, listener: ln, result: make(chan error, 1)}
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
	body, status, e := f.service.request(ctx, f.service.tokenEndpoint(), url.Values{"client_id": {id}, "code": {code}, "redirect_uri": {f.RedirectURI}, "code_verifier": {f.verifier}})
	if e != nil {
		return e
	}
	access := field(body, "access_token")
	if status < 200 || status > 299 || access == "" {
		return errors.New(message(body, "GitHub OAuth token exchange failed"))
	}
	t := Token{AccessToken: access, RefreshToken: field(body, "refresh_token"), ClientID: id, Scopes: parseScopes(field(body, "scope"))}
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
