// Package rodin is the Hyper3D web protocol: the REST procedures the
// hyper3d.ai web app calls on api.hyper3d.com/api, as typed methods. Nothing
// outside this package knows the wire shapes.
//
// The service answers most procedures with HTTP 200 or 201 and reports
// failures in the body as {"error": CODE}; do decodes both levels into the
// errors of errors.go. Signed file URLs (file.hyper3d.com) are fetched by
// Fetch without the bearer token and never appear in an error.
package rodin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultBaseURL is the web API every procedure lives under.
const DefaultBaseURL = "https://api.hyper3d.com/api"

// BaseURL is DefaultBaseURL, or RODIN_BASE_URL when set (tests point it at a
// local fake server).
func BaseURL() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("RODIN_BASE_URL")), "/"); v != "" {
		return v
	}
	return DefaultBaseURL
}

// Timeouts: API calls and file transfers.
const (
	apiTimeout  = 60 * time.Second
	fileTimeout = 10 * time.Minute
	// maxBody bounds an API reply read into memory.
	maxBody = 64 << 20
)

// transport is shared by every client in the process.
var transport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	MaxIdleConns:          16,
	MaxIdleConnsPerHost:   8,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   15 * time.Second,
	ResponseHeaderTimeout: apiTimeout,
	ForceAttemptHTTP2:     true,
}

// TokenSource supplies the bearer token. ForceRefresh is called once after a
// 401 with the token that was refused; it returns a token that differs from
// failed, or an error.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	ForceRefresh(ctx context.Context, failed string) (string, error)
}

// StaticToken is a TokenSource for one token that cannot be refreshed (the
// check right after a code exchange).
type StaticToken string

func (t StaticToken) Token(context.Context) (string, error) { return string(t), nil }
func (t StaticToken) ForceRefresh(context.Context, string) (string, error) {
	return "", ErrUnauthorized
}

// Client calls the web procedures.
type Client struct {
	base      string
	tokens    TokenSource
	userAgent string
	api       *http.Client
	files     *http.Client
}

// New returns a client for base (normally BaseURL()) authenticated by tokens.
// version goes into the User-Agent.
func New(base string, tokens TokenSource, version string) *Client {
	base = strings.TrimRight(base, "/")
	return &Client{
		base:      base,
		tokens:    tokens,
		userAgent: "rodin3d-mcp/" + version,
		api: &http.Client{
			Transport: transport,
			Timeout:   apiTimeout,
			// A redirect to another host would carry the bearer token away.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 0 && req.URL.Host != via[0].URL.Host {
					return errors.New("redirect to another host refused")
				}
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
		files: &http.Client{Transport: transport, Timeout: fileTimeout},
	}
}

// Base returns the API base URL of the client.
func (c *Client) Base() string { return c.base }

// request describes one procedure call.
type request struct {
	method      string
	procedure   string // e.g. "task/card/<uuid>"
	query       url.Values
	body        []byte
	contentType string
	noAuth      bool
}

// call runs req and decodes the reply into out (nil to discard). A 401 gets
// one forced token refresh and one retry: a 401 means the request was not
// processed, so it is safe to send again.
func (c *Client) call(ctx context.Context, req request, out any) error {
	token := ""
	if !req.noAuth {
		var err error
		if token, err = c.tokens.Token(ctx); err != nil {
			return err
		}
	}
	status, data, err := c.send(ctx, req, token)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized && !req.noAuth {
		fresh, rerr := c.tokens.ForceRefresh(ctx, token)
		if rerr != nil {
			if errors.Is(rerr, ErrUnauthorized) {
				return unauthorized(req.procedure, data)
			}
			return rerr
		}
		if status, data, err = c.send(ctx, req, fresh); err != nil {
			return err
		}
	}
	return decode(req.procedure, status, data, out)
}

// send performs one HTTP exchange and returns the status and body.
func (c *Client) send(ctx context.Context, req request, token string) (int, []byte, error) {
	u := c.base + "/" + req.procedure
	if len(req.query) > 0 {
		u += "?" + req.query.Encode()
	}
	var body io.Reader
	if req.body != nil {
		body = bytes.NewReader(req.body)
	}
	hr, err := http.NewRequestWithContext(ctx, req.method, u, body)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: build request: %w", req.procedure, err)
	}
	hr.Header.Set("Accept", "application/json")
	hr.Header.Set("User-Agent", c.userAgent)
	if req.contentType != "" {
		hr.Header.Set("Content-Type", req.contentType)
	}
	if token != "" {
		hr.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.api.Do(hr)
	if err != nil {
		return 0, nil, transportError(req.procedure, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return 0, nil, transportError(req.procedure, err)
	}
	if len(data) > maxBody {
		return 0, nil, fmt.Errorf("%s: reply larger than %d bytes", req.procedure, maxBody)
	}
	return resp.StatusCode, data, nil
}

// transportError drops the URL a *url.Error carries (it may hold query
// values) and keeps the cause, so messages name the procedure only.
func transportError(procedure string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return &NetError{Procedure: procedure, Err: ue.Err}
	}
	return &NetError{Procedure: procedure, Err: err}
}

// envelope is the error part every reply may carry.
type envelope struct {
	Error   json.RawMessage `json:"error"`
	Message json.RawMessage `json:"message"`
}

// decode turns a reply into out or one of the errors of errors.go.
func decode(procedure string, status int, data []byte, out any) error {
	var env envelope
	_ = json.Unmarshal(data, &env) // a body that is not an object has no envelope
	code := envString(env.Error)
	msg := envString(env.Message)
	switch {
	case status == http.StatusUnauthorized:
		return unauthorized(procedure, data)
	case status < 200 || status >= 300:
		return &HTTPError{Procedure: procedure, Status: status, Code: code, Message: msg}
	case code != "" && code != "OK":
		return &ServiceError{Procedure: procedure, Code: code, Message: msg}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: decode reply: %w", procedure, err)
	}
	return nil
}

// envString reads an envelope field that may be a string, a list of strings
// (NestJS validation messages) or absent.
func envString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return strings.Join(list, "; ")
	}
	return ""
}

func unauthorized(procedure string, data []byte) error {
	var env envelope
	_ = json.Unmarshal(data, &env)
	return &AuthError{Procedure: procedure, Code: envString(env.Error), Message: envString(env.Message)}
}

// postJSON sends body as JSON.
func (c *Client) postJSON(ctx context.Context, procedure string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s: encode body: %w", procedure, err)
	}
	return c.call(ctx, request{method: http.MethodPost, procedure: procedure, body: data, contentType: "application/json"}, out)
}
