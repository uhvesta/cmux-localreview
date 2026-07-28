package githubauth

import (
	"context"
	"errors"
	"strings"
)

// API is a route-neutral facade for daemon handlers. It contains only safe
// request/response values: public client IDs and browser URLs/codes may cross
// the loopback HTTP boundary; tokens, refresh tokens, and client secrets do
// not.
type API struct{ Service *ServiceClient }

type AuthStatus struct {
	Provider     string                `json:"provider"`
	Capabilities map[Capability]Status `json:"capabilities"`
}
type ConfigureRequest struct {
	Capability Capability `json:"capability"`
	ClientID   string     `json:"clientId"`
	// ClientSecret is accepted only over the daemon's authenticated loopback
	// API (normally from CLI stdin). It is immediately written to the OS secret
	// store and is never returned by this package or persisted in SQLite.
	ClientSecret string `json:"clientSecret,omitempty"`
}
type StartRequest struct {
	Capability Capability `json:"capability"`
	Flow       string     `json:"flow,omitempty"`
}
type StartResponse struct {
	Flow             string `json:"flow"`
	AuthorizationURL string `json:"authorizationUrl,omitempty"`
	VerificationURI  string `json:"verificationUri,omitempty"`
	UserCode         string `json:"userCode,omitempty"`
	ExpiresAt        int64  `json:"expiresAt,omitempty"`
}

func (api API) service() (*ServiceClient, error) {
	if api.Service == nil {
		return nil, errors.New("GitHub App authentication is unavailable")
	}
	return api.Service, nil
}
func (api API) Status(ctx context.Context) (AuthStatus, error) {
	s, e := api.service()
	if e != nil {
		return AuthStatus{}, e
	}
	out := AuthStatus{Provider: "github-app-device-flow", Capabilities: map[Capability]Status{}}
	for _, c := range []Capability{Read, Write, Copilot} {
		v, e := s.Status(ctx, c)
		if e != nil {
			return AuthStatus{}, e
		}
		out.Capabilities[c] = v
	}
	return out, nil
}
func (api API) Configure(ctx context.Context, request ConfigureRequest) error {
	_ = ctx
	s, e := api.service()
	if e != nil {
		return e
	}
	if err := s.Configure(request.Capability, request.ClientID); err != nil {
		return err
	}
	if strings.TrimSpace(request.ClientSecret) != "" {
		return s.SetClientSecret(request.Capability, request.ClientSecret)
	}
	return nil
}

// Start defaults to the registered loopback browser flow. The callback is
// stable, not dynamically allocated, so an OAuth App can register it exactly.
// Device flow remains the explicit SSH/headless fallback.
func (api API) Start(ctx context.Context, request StartRequest) (StartResponse, *LoopbackFlow, error) {
	s, e := api.service()
	if e != nil {
		return StartResponse{}, nil, e
	}
	flow := strings.TrimSpace(request.Flow)
	if flow == "" || flow == "loopback" {
		f, e := s.StartLoopback(ctx, request.Capability)
		if e != nil {
			return StartResponse{}, nil, e
		}
		return StartResponse{Flow: "loopback", AuthorizationURL: f.AuthorizationURL}, f, nil
	}
	if flow != "device" {
		return StartResponse{}, nil, errors.New("GitHub OAuth flow must be loopback or device")
	}
	v, e := s.Start(ctx, request.Capability)
	if e != nil {
		return StartResponse{}, nil, e
	}
	return StartResponse{Flow: "device", VerificationURI: v.VerificationURI, UserCode: v.UserCode, ExpiresAt: v.ExpiresAt}, nil, nil
}
func (api API) Poll(ctx context.Context, c Capability) (Status, error) {
	s, e := api.service()
	if e != nil {
		return Status{}, e
	}
	return s.Poll(ctx, c)
}
func (api API) Logout(ctx context.Context, c Capability) error {
	_ = ctx
	s, e := api.service()
	if e != nil {
		return e
	}
	return s.Disconnect(c)
}
