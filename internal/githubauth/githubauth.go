// Package githubauth provides the daemon-only credential boundary for the
// three deliberately separate cmux-localreview GitHub App capabilities.
// It never shells out to gh and never returns a token in a status response.
package githubauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const Service = "cmux-localreview.github-app"

type Capability string

const (
	Read    Capability = "read"
	Write   Capability = "write"
	Copilot Capability = "copilot"
)

func valid(c Capability) bool { return c == Read || c == Write || c == Copilot }

// SecretStore must be implemented by an OS credential provider. Implementations
// must not persist credentials in the daemon database or browser storage.
type SecretStore interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
	Delete(service, account string) error
}

// ConfigStore stores only public App client IDs. It intentionally has no token
// fields. FileConfigStore is a reasonable implementation outside this package.
type ConfigStore interface {
	ClientID(Capability) (string, error)
	SetClientID(Capability, string) error
}

type Opener func(string) error
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Token struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    int64  `json:"expiresAt,omitempty"`
	Login        string `json:"login,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
}

type Pending struct {
	DeviceCode      string
	ClientID        string
	UserCode        string
	VerificationURI string
	ExpiresAt       int64
	Interval        time.Duration
}

type StartResult struct {
	UserCode, VerificationURI string
	ExpiresAt                 int64
}
type Status struct {
	Configured    bool   `json:"configured"`
	Authenticated bool   `json:"authenticated"`
	Login         string `json:"login,omitempty"`
	State         string `json:"loginState"`
	Message       string `json:"message,omitempty"`
	Error         string `json:"error,omitempty"`
}

type ServiceClient struct {
	Secrets           SecretStore
	Config            ConfigStore
	HTTP              HTTPDoer
	Open              Opener
	Now               func() time.Time
	pending           map[Capability]Pending
	state             map[Capability]string
	message           map[Capability]string
	AuthorizeEndpoint string
	TokenEndpoint     string
}

func New(secrets SecretStore, config ConfigStore, httpClient HTTPDoer, open Opener) *ServiceClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if open == nil {
		open = func(string) error { return nil }
	}
	return &ServiceClient{Secrets: secrets, Config: config, HTTP: httpClient, Open: open, Now: time.Now, pending: map[Capability]Pending{}, state: map[Capability]string{}, message: map[Capability]string{}, AuthorizeEndpoint: "https://github.com/login/oauth/authorize", TokenEndpoint: "https://github.com/login/oauth/access_token"}
}

func account(c Capability) string { return "github.com:" + string(c) }
func (s *ServiceClient) clientID(c Capability) (string, error) {
	if !valid(c) {
		return "", errors.New("unknown GitHub capability")
	}
	id, err := s.Config.ClientID(c)
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("configure the %s GitHub App client ID before connecting it", c)
	}
	return id, nil
}

// Configure replaces a public client ID and invalidates its old app-owned
// token. A token issued to a prior App must never inherit the new authority.
func (s *ServiceClient) Configure(c Capability, id string) error {
	if !valid(c) {
		return errors.New("unknown GitHub capability")
	}
	id = strings.TrimSpace(id)
	if len(id) < 8 {
		return errors.New("GitHub App client ID looks invalid")
	}
	old, _ := s.Config.ClientID(c)
	if err := s.Config.SetClientID(c, id); err != nil {
		return err
	}
	if old != "" && old != id {
		if err := s.Secrets.Delete(Service, account(c)); err != nil {
			return err
		}
	}
	delete(s.pending, c)
	s.state[c] = "idle"
	delete(s.message, c)
	return nil
}

func (s *ServiceClient) request(ctx context.Context, endpoint string, values url.Values) (map[string]any, int, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, 0, err
	}
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.HTTP.Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	var out map[string]any
	if json.Unmarshal(b, &out) != nil {
		out = map[string]any{}
	}
	return out, resp.StatusCode, nil
}
func field(m map[string]any, key string) string { v, _ := m[key].(string); return v }
func message(m map[string]any, fallback string) string {
	if x := field(m, "error_description"); x != "" {
		return x
	}
	if x := field(m, "error"); x != "" {
		return x
	}
	return fallback
}

func (s *ServiceClient) Start(ctx context.Context, c Capability) (StartResult, error) {
	id, err := s.clientID(c)
	if err != nil {
		return StartResult{}, err
	}
	body, code, err := s.request(ctx, "https://github.com/login/device/code", url.Values{"client_id": {id}})
	if err != nil {
		return StartResult{}, err
	}
	device, user, uri := field(body, "device_code"), field(body, "user_code"), field(body, "verification_uri")
	if code < 200 || code > 299 || device == "" || user == "" || uri == "" {
		return StartResult{}, errors.New(message(body, fmt.Sprintf("GitHub device authorization failed (%d)", code)))
	}
	expires := s.Now().Add(time.Duration(number(body["expires_in"], 900)) * time.Second)
	p := Pending{DeviceCode: device, ClientID: id, UserCode: user, VerificationURI: uri, ExpiresAt: expires.UnixMilli(), Interval: time.Duration(number(body["interval"], 5)) * time.Second}
	if p.Interval < time.Second {
		p.Interval = time.Second
	}
	s.pending[c] = p
	s.state[c] = "waiting"
	s.message[c] = "Complete GitHub authorization in the browser, then return here."
	if err := s.Open(uri); err != nil {
		return StartResult{}, err
	}
	return StartResult{UserCode: user, VerificationURI: uri, ExpiresAt: p.ExpiresAt}, nil
}
func number(v any, fallback int64) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return fallback
}

func (s *ServiceClient) Poll(ctx context.Context, c Capability) (Status, error) {
	p, ok := s.pending[c]
	if !ok {
		return s.Status(ctx, c)
	}
	if s.Now().UnixMilli() >= p.ExpiresAt {
		delete(s.pending, c)
		s.state[c] = "failed"
		s.message[c] = "This GitHub authorization code expired. Start again."
		return s.Status(ctx, c)
	}
	body, code, err := s.request(ctx, s.tokenEndpoint(), url.Values{"client_id": {p.ClientID}, "device_code": {p.DeviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}})
	if err != nil {
		return Status{}, err
	}
	if field(body, "error") == "authorization_pending" || field(body, "error") == "slow_down" {
		return s.Status(ctx, c)
	}
	access := field(body, "access_token")
	if code < 200 || code > 299 || access == "" {
		delete(s.pending, c)
		s.state[c] = "failed"
		s.message[c] = message(body, "GitHub authorization failed.")
		return s.Status(ctx, c)
	}
	t := Token{AccessToken: access, RefreshToken: field(body, "refresh_token"), ClientID: p.ClientID}
	if secs := number(body["expires_in"], 0); secs > 0 {
		t.ExpiresAt = s.Now().Add(time.Duration(secs) * time.Second).UnixMilli()
	}
	login, err := s.viewer(ctx, access)
	if err != nil {
		return Status{}, err
	}
	t.Login = login
	if err = s.write(c, t); err != nil {
		return Status{}, err
	}
	delete(s.pending, c)
	s.state[c] = "succeeded"
	s.message[c] = "Connected as @" + login + "."
	return s.Status(ctx, c)
}

func (s *ServiceClient) viewer(ctx context.Context, token string) (string, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept", "application/vnd.github+json")
	resp, err := s.HTTP.Do(r)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	login := field(out, "login")
	if resp.StatusCode < 200 || resp.StatusCode > 299 || login == "" {
		return "", errors.New(message(out, "GitHub could not verify this account."))
	}
	return login, nil
}
func (s *ServiceClient) read(c Capability) (Token, error) {
	raw, err := s.Secrets.Get(Service, account(c))
	if err != nil {
		return Token{}, err
	}
	if raw == "" {
		return Token{}, fmt.Errorf("the %s GitHub App is not connected", c)
	}
	var t Token
	if json.Unmarshal([]byte(raw), &t) != nil || t.AccessToken == "" {
		return Token{}, errors.New("stored GitHub credential is invalid")
	}
	return t, nil
}
func (s *ServiceClient) write(c Capability, t Token) error {
	b, _ := json.Marshal(t)
	return s.Secrets.Set(Service, account(c), string(b))
}

// Token returns an app-owned access token for daemon code only, refreshing it
// when GitHub issued a refresh token. UI callers must use Status, never Token.
func (s *ServiceClient) Token(ctx context.Context, c Capability) (string, error) {
	t, err := s.read(c)
	if err != nil {
		return "", err
	}
	id, err := s.clientID(c)
	if err != nil {
		return "", err
	}
	if t.ClientID != "" && t.ClientID != id {
		_ = s.Secrets.Delete(Service, account(c))
		return "", fmt.Errorf("the %s GitHub App registration changed; connect it again", c)
	}
	if t.ExpiresAt == 0 || t.ExpiresAt > s.Now().Add(time.Minute).UnixMilli() {
		return t.AccessToken, nil
	}
	if t.RefreshToken == "" {
		return "", fmt.Errorf("the %s GitHub App token expired; connect it again", c)
	}
	body, code, err := s.request(ctx, s.tokenEndpoint(), url.Values{"client_id": {id}, "grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}})
	if err != nil {
		return "", err
	}
	next := field(body, "access_token")
	if code < 200 || code > 299 || next == "" {
		return "", errors.New(message(body, "GitHub App token could not refresh"))
	}
	t.AccessToken = next
	t.RefreshToken = field(body, "refresh_token")
	if secs := number(body["expires_in"], 0); secs > 0 {
		t.ExpiresAt = s.Now().Add(time.Duration(secs) * time.Second).UnixMilli()
	}
	t.ClientID = id
	if err := s.write(c, t); err != nil {
		return "", err
	}
	return t.AccessToken, nil
}
func (s *ServiceClient) Disconnect(c Capability) error {
	if !valid(c) {
		return errors.New("unknown GitHub capability")
	}
	if err := s.Secrets.Delete(Service, account(c)); err != nil {
		return err
	}
	delete(s.pending, c)
	s.state[c] = "idle"
	s.message[c] = "Disconnected locally. Revoke the App installation in GitHub settings too."
	return nil
}
func (s *ServiceClient) Status(ctx context.Context, c Capability) (Status, error) {
	if !valid(c) {
		return Status{}, errors.New("unknown GitHub capability")
	}
	id, err := s.Config.ClientID(c)
	if err != nil {
		return Status{}, err
	}
	out := Status{Configured: strings.TrimSpace(id) != "", State: s.state[c], Message: s.message[c]}
	if out.State == "" {
		out.State = "idle"
	}
	if !out.Configured {
		out.Error = "Configure a dedicated GitHub App for " + string(c) + "."
		return out, nil
	}
	t, e := s.read(c)
	if e != nil {
		return out, nil
	}
	login, e := s.viewer(ctx, t.AccessToken)
	if e != nil {
		out.Error = e.Error()
		return out, nil
	}
	out.Authenticated = true
	out.Login = login
	return out, nil
}

// Keep bytes imported on older Go toolchains where url.Values encoding is inlined
// differently by test instrumentation.
var _ = bytes.MinRead
