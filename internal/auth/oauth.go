// Package auth signs rodin3d-mcp in to Hyper3D with its own OAuth client:
// a public native client registered by dynamic client registration, the
// authorization code flow with PKCE (S256), and a loopback redirect. The user
// approves in any browser on any device; the redirect reaches the loopback
// listener when that browser runs on this machine, otherwise the user pastes
// the final address back (notes 15, plan D1 and D20).
//
// Tokens live in credentials.json (secret.FileStore, 0600). Every refresh
// runs under a file lock so processes never race a refresh-token rotation.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Scope is what the client asks for; offline_access yields a refresh token
// and frontend:access opens the web procedures.
const Scope = "openid offline_access frontend:access"

// ClientName is the name the consent page shows.
const ClientName = "rodin3d-mcp"

// Ports are the loopback ports registered as redirect URIs; a login binds the
// first free one.
var Ports = []int{53682, 53683, 53684, 53685, 53686}

// RedirectURI is the registered redirect for port.
func RedirectURI(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) + "/callback" }

// Endpoints are the authorization server and the resource it issues tokens
// for, derived from the API base so a test server stands in for both.
type Endpoints struct {
	Issuer   string // https://api.hyper3d.com/api/grant/oauth
	Resource string // https://api.hyper3d.com/api
}

// EndpointsFor derives the endpoints from an API base URL.
func EndpointsFor(apiBase string) Endpoints {
	apiBase = strings.TrimRight(apiBase, "/")
	return Endpoints{Issuer: apiBase + "/grant/oauth", Resource: apiBase}
}

// TokenSet is a token endpoint reply. The id_token is not kept.
type TokenSet struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
}

// OAuthError is an error reply of the authorization server.
type OAuthError struct {
	Endpoint    string
	Status      int
	Code        string // invalid_grant, invalid_client, ...
	Description string
}

func (e *OAuthError) Error() string {
	s := fmt.Sprintf("%s: HTTP %d", e.Endpoint, e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Description != "" {
		s += ": " + e.Description
	}
	return s
}

// OAuth calls the authorization server.
type OAuth struct {
	ep        Endpoints
	http      *http.Client
	userAgent string
}

// NewOAuth returns a client for ep.
func NewOAuth(ep Endpoints, version string) *OAuth {
	return &OAuth{
		ep:        ep,
		userAgent: "rodin3d-mcp/" + version,
		http: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Endpoints returns the endpoints of o.
func (o *OAuth) Endpoints() Endpoints { return o.ep }

// Register creates a public native client and returns its id.
func (o *OAuth) Register(ctx context.Context) (string, error) {
	redirects := make([]string, len(Ports))
	for i, p := range Ports {
		redirects[i] = RedirectURI(p)
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                ClientName,
		"application_type":           "native",
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"redirect_uris":              redirects,
		"scope":                      Scope,
	})
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := o.post(ctx, "/oauth2/register", "application/json", string(body), &out); err != nil {
		return "", err
	}
	if out.ClientID == "" {
		return "", errors.New("oauth2/register: the reply has no client_id")
	}
	return out.ClientID, nil
}

// PKCE is one authorization attempt's secrets.
type PKCE struct {
	State     string
	Verifier  string
	Challenge string
}

// NewPKCE draws a 128-bit state and a 256-bit verifier.
func NewPKCE() (PKCE, error) {
	state := make([]byte, 16)
	verifier := make([]byte, 32)
	if _, err := rand.Read(state); err != nil {
		return PKCE{}, err
	}
	if _, err := rand.Read(verifier); err != nil {
		return PKCE{}, err
	}
	v := base64.RawURLEncoding.EncodeToString(verifier)
	sum := sha256.Sum256([]byte(v))
	return PKCE{State: hex.EncodeToString(state), Verifier: v, Challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

// AuthorizeURL is the address the user opens to sign in and approve.
func (o *OAuth) AuthorizeURL(clientID, redirectURI string, p PKCE) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", Scope)
	q.Set("state", p.State)
	q.Set("code_challenge", p.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("resource", o.ep.Resource)
	return o.ep.Issuer + "/oauth2/authorize?" + q.Encode()
}

// Exchange trades an authorization code for tokens.
func (o *OAuth) Exchange(ctx context.Context, clientID, redirectURI, code, verifier string) (*TokenSet, error) {
	return o.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
		"resource":      {o.ep.Resource},
	})
}

// Refresh trades a refresh token for new tokens; the refresh token rotates.
func (o *OAuth) Refresh(ctx context.Context, clientID, refreshToken string) (*TokenSet, error) {
	return o.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	})
}

// Revoke ends the grant session of refreshToken. The access token is a JWT
// the server cannot revoke; it lapses at its expiry.
func (o *OAuth) Revoke(ctx context.Context, clientID, refreshToken string) error {
	form := url.Values{"token": {refreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {clientID}}
	return o.post(ctx, "/oauth2/revoke", "application/x-www-form-urlencoded", form.Encode(), nil)
}

func (o *OAuth) token(ctx context.Context, form url.Values) (*TokenSet, error) {
	var out TokenSet
	if err := o.post(ctx, "/oauth2/token", "application/x-www-form-urlencoded", form.Encode(), &out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, errors.New("oauth2/token: the reply has no access_token")
	}
	return &out, nil
}

// post sends body to an endpoint of the issuer and decodes a 2xx reply into
// out. Errors name the endpoint and the server's error code, never a token.
func (o *OAuth) post(ctx context.Context, path, contentType, body string, out any) error {
	endpoint := strings.TrimPrefix(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.ep.Issuer+path, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: build request: %w", endpoint, err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", o.userAgent)
	resp, err := o.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%s: read reply: %w", endpoint, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(data, &e)
		return &OAuthError{Endpoint: endpoint, Status: resp.StatusCode, Code: e.Error, Description: e.Description}
	}
	if out == nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: decode reply: %w", endpoint, err)
	}
	return nil
}
