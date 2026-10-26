// Package fakehyper is a local stand-in for Hyper3D used by the tests: the
// OAuth server (registration, code + PKCE, refresh with rotation, revoke)
// and the web procedures the client calls, with replies shaped like the
// recorded ones (notes 13, 14, 15). File addresses carry a fake signature
// ("sig=SIGNATURE-...") so tests can assert it never leaks into output.
//
// It never contacts Hyper3D.
package fakehyper

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SignatureMark starts every fake signature; output must never contain it.
const SignatureMark = "SIGNATURE-"

// State is a step and user_state pair.
type State struct {
	Step      string
	UserState string
	Pending   int
}

// Task is one fake Rodin task.
type Task struct {
	UUID      string
	Prompt    string
	Created   string
	Step      string
	UserState string
	Pending   int
	Private   bool
	Settings  map[string]any
	Inputs    []string          // mesh_input_images names
	Poster    map[string]string // card_poster_info key -> file name
	Preview   map[string]string // model_preview_info key -> file name
	Confirmed map[string]any    // confirmation_metadata
	// GeoVersions are the rodin_history ModelGenerate keys; MatVersions the
	// TextureGenerate keys per reference.
	GeoVersions []string
	MatVersions map[string][]string
	Pack        []string // get_download names; nil: not packed (INVALID_REQUEST)
	// Next states are applied one per card call, then the last one stays.
	Next []State
}

// Server is the fake.
type Server struct {
	HTTP *httptest.Server

	mu        sync.Mutex
	tasks     map[string]*Task
	order     []string
	files     map[string][]byte // path under /files/ -> bytes
	clients   map[string][]string
	codes     map[string]grant
	access    map[string]tokenInfo
	refresh   map[string]string // refresh token -> grant id
	revoked   map[string]bool   // grant ids
	calls     map[string]int
	refreshes int
	// AccessTTL is the lifetime of issued access tokens (seconds).
	AccessTTL int
	// Reject401 makes the next n API calls answer 401 regardless of the token.
	Reject401 int
	// User is the account get_info reports.
	User User
	// BearerOnFile records a file request that carried an Authorization header.
	BearerOnFile bool
}

// User is the fake account.
type User struct {
	UUID, Username, Email string
	Balance, Subscription float64 // tenths
	Frozen                float64
	Plan                  string
	Privileges            []string
}

type grant struct {
	client, redirect, challenge, resource string
}

type tokenInfo struct {
	grant   string
	expires time.Time
}

// New starts the fake with no tasks.
func New() *Server {
	s := &Server{
		tasks: map[string]*Task{}, files: map[string][]byte{}, clients: map[string][]string{},
		codes: map[string]grant{}, access: map[string]tokenInfo{}, refresh: map[string]string{},
		revoked: map[string]bool{}, calls: map[string]int{}, AccessTTL: 3600,
		User: User{
			UUID: "u-0000-0000", Username: "owner", Email: "owner@example.com", Balance: 250, Subscription: 285, Plan: "Creator",
			Privileges: []string{"RodinDownloadModel", "RodinUseMultiImage", "RodinUseAdditionalImage", "RodinMediumRedoCount",
				"RodinMeshAdditionalFeatures", "RodinRemeshAdditionalFeatures", "RodinTextureAdditionalFeatures", "RodinBangGenerateAdvancedQuality"},
		},
	}
	s.HTTP = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Close stops the fake.
func (s *Server) Close() { s.HTTP.Close() }

// Base is the API base URL (what RODIN_BASE_URL would hold).
func (s *Server) Base() string { return s.HTTP.URL + "/api" }

// Issuer is the authorization server's issuer.
func (s *Server) Issuer() string { return s.Base() + "/grant/oauth" }

// AddTask registers a task (newest last).
func (s *Server) AddTask(t *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.Settings == nil {
		t.Settings = map[string]any{}
	}
	s.tasks[t.UUID] = t
	s.order = append(s.order, t.UUID)
}

// SetFile serves data at /files/<path>.
func (s *Server) SetFile(path string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = data
}

// Calls returns how often a procedure was called.
func (s *Server) Calls(procedure string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[procedure]
}

// Refreshes returns how many refresh grants were served.
func (s *Server) Refreshes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshes
}

// ExpireAccess makes every issued access token expired.
func (s *Server) ExpireAccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.access {
		v.expires = time.Now().Add(-time.Second)
		s.access[k] = v
	}
}

// RevokeAll ends every grant (as a revoke or a lapsed refresh token would).
func (s *Server) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.refresh {
		s.revoked[g] = true
	}
}

// GrantRevoked reports whether any grant was revoked.
func (s *Server) GrantRevoked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.revoked) > 0
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Approve plays the user's browser: it takes the authorize URL, checks it the
// way the server would, and returns the final redirect address.
func (s *Server) Approve(authorizeURL string) (string, error) {
	u, err := url.Parse(authorizeURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	redirects, ok := s.clients[q.Get("client_id")]
	if !ok {
		return "", fmt.Errorf("unknown client")
	}
	allowed := false
	for _, r := range redirects {
		if r == q.Get("redirect_uri") {
			allowed = true
		}
	}
	switch {
	case !allowed:
		return "", fmt.Errorf("redirect_uri not registered")
	case q.Get("response_type") != "code", q.Get("code_challenge_method") != "S256", q.Get("code_challenge") == "":
		return "", fmt.Errorf("bad authorize request")
	case q.Get("scope") != "openid offline_access frontend:access":
		return "", fmt.Errorf("unexpected scope %q", q.Get("scope"))
	}
	code := randHex(16)
	s.codes[code] = grant{client: q.Get("client_id"), redirect: q.Get("redirect_uri"), challenge: q.Get("code_challenge"), resource: q.Get("resource")}
	return q.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {q.Get("state")}, "iss": {s.Issuer()}}.Encode(), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/files/"):
		s.serveFile(w, r)
	case strings.HasPrefix(p, "/api/grant/oauth/"):
		s.serveOAuth(w, r, strings.TrimPrefix(p, "/api/grant/oauth"))
	case strings.HasPrefix(p, "/api/"):
		s.serveAPI(w, r, strings.TrimPrefix(p, "/api/"))
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if r.Header.Get("Authorization") != "" {
		s.BearerOnFile = true
	}
	data, ok := s.files[strings.TrimPrefix(r.URL.Path, "/files/")]
	s.mu.Unlock()
	if !strings.HasPrefix(r.URL.Query().Get("sig"), SignatureMark) {
		http.Error(w, "missing signature", http.StatusForbidden)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(data)
}

func (s *Server) fileURL(path string) string {
	return s.HTTP.URL + "/files/" + path + "?sig=" + SignatureMark + randHex(6) + "&exp=1"
}

func (s *Server) serveOAuth(w http.ResponseWriter, r *http.Request, p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	switch p {
	case "/oauth2/register":
		var req struct {
			RedirectURIs []string `json:"redirect_uris"`
			Auth         string   `json:"token_endpoint_auth_method"`
			GrantTypes   []string `json:"grant_types"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Auth != "none" {
			writeJSON(w, 400, map[string]string{"error": "invalid_client_metadata"})
			return
		}
		id := "client-" + randHex(6)
		s.clients[id] = req.RedirectURIs
		writeJSON(w, 201, map[string]any{"client_id": id, "grant_types": []string{"authorization_code", "refresh_token"}})
	case "/oauth2/token":
		form, _ := url.ParseQuery(string(body))
		switch form.Get("grant_type") {
		case "authorization_code":
			g, ok := s.codes[form.Get("code")]
			delete(s.codes, form.Get("code"))
			sum := sha256.Sum256([]byte(form.Get("code_verifier")))
			switch {
			case !ok:
				writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "code not found"})
			case g.client != form.Get("client_id") || g.redirect != form.Get("redirect_uri"):
				writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "client or redirect mismatch"})
			case base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
				writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "PKCE verification failed"})
			case form.Get("resource") != s.Base():
				writeJSON(w, 400, map[string]string{"error": "invalid_target"})
			default:
				writeJSON(w, 200, s.issue("grant-"+randHex(4)))
			}
		case "refresh_token":
			s.refreshes++
			gid, ok := s.refresh[form.Get("refresh_token")]
			if !ok || s.revoked[gid] {
				writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "session not found"})
				return
			}
			writeJSON(w, 200, s.issue(gid))
		default:
			writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
		}
	case "/oauth2/revoke":
		form, _ := url.ParseQuery(string(body))
		tok := form.Get("token")
		if _, ok := s.access[tok]; ok {
			writeJSON(w, 400, map[string]string{"error": "unsupported_token_type", "error_description": "JWT access tokens are self-contained and cannot be revoked server-side"})
			return
		}
		if gid, ok := s.refresh[tok]; ok {
			s.revoked[gid] = true
		}
		writeJSON(w, 200, map[string]any{})
	default:
		writeJSON(w, 404, map[string]string{"error": "not_found"})
	}
}

// issue mints an access and a rotated refresh token for grant gid.
func (s *Server) issue(gid string) map[string]any {
	access := "at." + randHex(24)
	refresh := randHex(16)
	s.access[access] = tokenInfo{grant: gid, expires: time.Now().Add(time.Duration(s.AccessTTL) * time.Second)}
	s.refresh[refresh] = gid
	return map[string]any{"access_token": access, "expires_in": s.AccessTTL, "token_type": "Bearer", "refresh_token": refresh,
		"scope": "openid offline_access frontend:access", "id_token": "id." + randHex(8)}
}

func (s *Server) authorized(r *http.Request) bool {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	info, ok := s.access[tok]
	return ok && time.Now().Before(info.expires) && !s.revoked[info.grant]
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request, proc string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := proc // procedures with an id in the path count under their prefix
	for _, prefix := range []string{"task/card/", "task/check_progress/", "task/list_Rodin/"} {
		if strings.HasPrefix(proc, prefix) {
			name = strings.TrimSuffix(prefix, "/")
		}
	}
	s.calls[name]++
	if s.Reject401 > 0 {
		s.Reject401--
		writeJSON(w, 401, map[string]any{"statusCode": 401, "message": "Invalid authorization header"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, 401, map[string]any{"statusCode": 401, "message": "Invalid authorization header"})
		return
	}
	body, _ := io.ReadAll(r.Body)
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	str := func(k string) string { v, _ := in[k].(string); return v }

	switch {
	case proc == "user/get_info":
		u := s.User
		writeJSON(w, 201, map[string]any{"meta": map[string]any{
			"user_uuid": u.UUID, "username": u.Username, "email": u.Email, "balance": u.Balance, "frozen": u.Frozen,
			"prepaid_available": u.Balance + u.Subscription, "privileges": u.Privileges, "features": []string{"RodinTextureAdditionalFeatures"},
			"subscriptions": map[string]any{"current_active": true, "highest_level": u.Plan, "active_subscriptions": []any{
				map[string]any{"active": true, "plan_level": u.Plan, "wallet_balance": u.Subscription, "start_time": "2026-10-16T00:00:00.000Z", "end_time": "2026-11-16T00:00:00.000Z"},
			}},
		}})
	case strings.HasPrefix(proc, "task/list_Rodin/"):
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if steps := r.URL.Query()["step"]; len(steps) != 4 {
			writeJSON(w, 400, map[string]any{"statusCode": 400, "message": []string{"step must be one of the Rodin steps"}})
			return
		}
		ids := append([]string(nil), s.order...)
		sort.SliceStable(ids, func(i, j int) bool { return s.tasks[ids[i]].Created > s.tasks[ids[j]].Created })
		total := len(ids)
		start := min((page-1)*limit, total)
		end := min(start+limit, total)
		var rows []any
		for _, id := range ids[start:end] {
			t := s.tasks[id]
			rows = append(rows, map[string]any{"task_uuid": t.UUID, "type": "Rodin", "prompt": t.Prompt, "time": t.Created, "step": t.Step,
				"is_private": t.Private, "is_hidden": false, "pending_jobs": make([]string, t.Pending), "num_like": 0,
				"author": map[string]any{"username": s.User.Username, "email": s.User.Email}, "image_url": "", "video_url": ""})
		}
		pages := (total + limit - 1) / limit
		writeJSON(w, 200, map[string]any{"pagination": map[string]any{"total": total, "page": page, "limit": limit, "total_pages": pages}, "data": rows})
	case strings.HasPrefix(proc, "task/card/"):
		t, ok := s.tasks[strings.TrimPrefix(proc, "task/card/")]
		if !ok {
			writeJSON(w, 201, map[string]any{"error": "NO_SUCH_TASK"})
			return
		}
		if len(t.Next) > 0 {
			n := t.Next[0]
			t.Step, t.UserState, t.Pending = n.Step, n.UserState, n.Pending
			if len(t.Next) > 1 {
				t.Next = t.Next[1:]
			} else {
				t.Next = nil
			}
		}
		writeJSON(w, 201, s.card(t))
	case strings.HasPrefix(proc, "task/check_progress/"):
		writeJSON(w, 201, map[string]any{"stage": "Done"})
	case proc == "task/rodin_history":
		t, ok := s.tasks[str("uuid")]
		if !ok {
			writeJSON(w, 201, map[string]any{"error": "NO_SUCH_TASK"})
			return
		}
		switch str("step") {
		case "ModelGenerate":
			out := map[string]any{}
			for _, v := range t.GeoVersions {
				out[v] = map[string]any{"preview_cover": s.fileURL(t.UUID + "/geo-" + v + ".webp"), "preview_drc": s.fileURL(t.UUID + "/geo-" + v + ".drc"),
					"settings": s.fileURL(t.UUID + "/geo-" + v + ".json"), "generation_time": "2026-10-01T10:00:00.000Z", "job_uuid": "job-" + v}
			}
			if len(out) == 0 {
				writeJSON(w, 201, map[string]any{"error": "NO_HISTORY_AVAILABLE"})
				return
			}
			writeJSON(w, 201, out)
		case "TextureGenerate":
			out := map[string]any{}
			for ref, vs := range t.MatVersions {
				m := map[string]any{}
				for _, v := range vs {
					m[v] = map[string]any{"preview_image": s.fileURL(t.UUID + "/mat-" + v + ".jpg"), "texture_diffuse": s.fileURL(t.UUID + "/diffuse-" + v + ".jpg"),
						"texture_pbr": s.fileURL(t.UUID + "/pbr-" + v + ".jpg"), "texture_normal": s.fileURL(t.UUID + "/normal-" + v + ".jpg"),
						"texture_shaded": s.fileURL(t.UUID + "/shaded-" + v + ".jpg"), "settings": s.fileURL(t.UUID + "/mat.json")}
				}
				out[ref] = m
			}
			writeJSON(w, 201, out)
		default:
			writeJSON(w, 201, map[string]any{"error": "NO_HISTORY_AVAILABLE"})
		}
	case proc == "task/get_download":
		t, ok := s.tasks[str("task_uuid")]
		if !ok || t.Pack == nil {
			writeJSON(w, 201, map[string]any{"error": "INVALID_REQUEST"})
			return
		}
		list := []any{}
		for _, n := range t.Pack {
			list = append(list, map[string]any{"name": n, "url": s.fileURL(t.UUID + "/pack/" + n)})
		}
		writeJSON(w, 201, map[string]any{"list": list})
	case proc == "task/rodin_mesh_image_process":
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			writeJSON(w, 400, map[string]any{"statusCode": 400, "message": "multipart expected"})
			return
		}
		writeJSON(w, 201, map[string]any{"prompt": "A figure. sharp edges", "height": 165, "allow_face_enhance": true, "default_face_enhance": false, "is_multiview": false, "num_multiview": 1})
	default:
		writeJSON(w, 404, map[string]any{"statusCode": 404, "message": "Cannot POST /api/" + proc})
	}
}

func (s *Server) card(t *Task) map[string]any {
	inputs := map[string]any{}
	for _, n := range t.Inputs {
		inputs[n] = s.fileURL(t.UUID + "/in-" + n)
	}
	poster := map[string]any{}
	for k, f := range t.Poster {
		poster[k] = s.fileURL(t.UUID + "/" + f)
	}
	preview := map[string]any{}
	for k, f := range t.Preview {
		preview[k] = s.fileURL(t.UUID + "/" + f)
	}
	info := map[string]any{"conditions": s.fileURL(t.UUID + "/conditions.json"), "smart_uvs": []any{}, "mesh_input_images": inputs,
		"mesh_reference_images": []any{}, "material_input_images": map[string]any{}, "is_bang": false}
	if t.Confirmed != nil {
		info["confirmation_metadata"] = t.Confirmed
	}
	return map[string]any{
		"task_uuid": t.UUID, "type": "Rodin", "is_agentic_prepare": false, "prompt": t.Prompt, "num_like": 0, "is_like": false,
		"time": t.Created, "author": map[string]any{"username": s.User.Username, "email": s.User.Email}, "is_private": t.Private, "is_hidden": false,
		"step": t.Step, "pending_jobs": make([]string, t.Pending), "image_url": "", "video_url": "",
		"settings": t.Settings, "rodin_generate_info": info, "card_poster_info": poster, "model_preview_info": preview,
		"user_state": t.UserState, "generate_time": "2026-10-01T10:05:00.000Z", "is_featured": false,
	}
}
