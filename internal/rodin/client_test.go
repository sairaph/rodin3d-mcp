package rodin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// tokens is a TokenSource that hands out "t1" and refreshes to "t2".
type tokens struct {
	mu        sync.Mutex
	current   string
	refreshes int
	refuse    bool
}

func (t *tokens) Token(context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current, nil
}

func (t *tokens) ForceRefresh(_ context.Context, failed string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshes++
	if t.refuse {
		return "", ErrUnauthorized
	}
	t.current = "t2"
	return t.current, nil
}

func server(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestBodyErrorIsServiceError(t *testing.T) {
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		w.Write([]byte(`{"error":"TASK_ALREADY_CONFIRMED"}`))
	})
	c := New(s.URL, &tokens{current: "t1"}, "test")
	_, err := c.Card(context.Background(), "x")
	var se *ServiceError
	if !errors.As(err, &se) || se.Code != "TASK_ALREADY_CONFIRMED" || se.Text() == "" {
		t.Fatalf("err = %v", err)
	}
}

func TestUnauthorizedRefreshesOnceAndRetries(t *testing.T) {
	var seen []string
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer t2" {
			w.WriteHeader(401)
			w.Write([]byte(`{"statusCode":401,"message":"Invalid authorization header"}`))
			return
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"meta":{"user_uuid":"u1","username":"me","prepaid_available":535}}`))
	})
	tok := &tokens{current: "t1"}
	info, err := New(s.URL, tok, "test").GetInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Meta.UserUUID != "u1" || tok.refreshes != 1 || len(seen) != 2 {
		t.Fatalf("info %+v refreshes %d requests %v", info.Meta, tok.refreshes, seen)
	}
}

func TestUnauthorizedTwiceIsAuthError(t *testing.T) {
	calls := 0
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"UNAUTHORIZED","message":"Logged in is required."}`))
	})
	tok := &tokens{current: "t1"}
	_, err := New(s.URL, tok, "test").GetInfo(context.Background())
	var ae *AuthError
	if !errors.As(err, &ae) || !errors.Is(err, ErrUnauthorized) || calls != 2 || tok.refreshes != 1 {
		t.Fatalf("err %v calls %d refreshes %d", err, calls, tok.refreshes)
	}
	tok = &tokens{current: "t1", refuse: true}
	calls = 0
	_, err = New(s.URL, tok, "test").GetInfo(context.Background())
	if !errors.Is(err, ErrUnauthorized) || calls != 1 {
		t.Fatalf("refused refresh: err %v calls %d", err, calls)
	}
}

// Hyper3D refuses a session it does not accept on user-scoped routes with
// HTTP 200/201 and a body error code; that is treated like a 401.
func TestBodyLevelRefusalRefreshesOnceAndRetries(t *testing.T) {
	for _, code := range []string{"NO_CURRENT_USER", "UNAUTHORIZED", "USER_NOT_FOUND"} {
		var seen []string
		s := server(t, func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, r.Header.Get("Authorization"))
			w.WriteHeader(201)
			if r.Header.Get("Authorization") != "Bearer t2" {
				w.Write([]byte(`{"error":"` + code + `"}`))
				return
			}
			w.Write([]byte(`{"meta":{"user_uuid":"u1","username":"me"}}`))
		})
		tok := &tokens{current: "t1"}
		info, err := New(s.URL, tok, "test").GetInfo(context.Background())
		if err != nil || info.Meta.UserUUID != "u1" || tok.refreshes != 1 || len(seen) != 2 || seen[1] != "Bearer t2" {
			t.Fatalf("%s: info %+v err %v refreshes %d requests %v", code, info, err, tok.refreshes, seen)
		}
	}
}

func TestBodyLevelRefusalTwiceIsAuthError(t *testing.T) {
	calls := 0
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(200)
		w.Write([]byte(`{"error":"NO_CURRENT_USER"}`))
	})
	tok := &tokens{current: "t1"}
	_, err := New(s.URL, tok, "test").ListMine(context.Background(), "u", 1, 10)
	var ae *AuthError
	if !errors.As(err, &ae) || !errors.Is(err, ErrUnauthorized) || ae.Code != "NO_CURRENT_USER" || calls != 2 || tok.refreshes != 1 {
		t.Fatalf("err %v calls %d refreshes %d", err, calls, tok.refreshes)
	}
	// Another body error on a 2xx stays a service error, without a refresh.
	tok = &tokens{current: "t1"}
	s2 := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		w.Write([]byte(`{"error":"NO_SUCH_TASK"}`))
	})
	_, err = New(s2.URL, tok, "test").Card(context.Background(), "x")
	var se *ServiceError
	if !errors.As(err, &se) || errors.Is(err, ErrUnauthorized) || tok.refreshes != 0 {
		t.Fatalf("NO_SUCH_TASK: err %v refreshes %d", err, tok.refreshes)
	}
}

func TestHTTPErrorKeepsStatusAndMessage(t *testing.T) {
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"statusCode":400,"message":["step must be one of"]}`))
	})
	_, err := New(s.URL, &tokens{current: "t"}, "test").ListMine(context.Background(), "u", 1, 10)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 400 || he.Message != "step must be one of" {
		t.Fatalf("err = %#v", err)
	}
}

func TestListSendsTheWebQuery(t *testing.T) {
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/task/list_Rodin/Mine" || len(q["step"]) != 4 || q.Get("hidden") != "false" || q.Get("user_uuid") != "u" {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"pagination":{"total":1,"page":1,"limit":10,"total_pages":1},"data":[{"task_uuid":"a","step":"ModelGenerate","pending_jobs":null}]}`))
	})
	page, err := New(s.URL, &tokens{current: "t"}, "test").ListMine(context.Background(), "u", 1, 10)
	if err != nil || len(page.Data) != 1 || page.Data[0].PendingJobs != 0 {
		t.Fatalf("page %+v err %v", page, err)
	}
}

func TestCardToleratesShapes(t *testing.T) {
	raw := `{"task_uuid":"a","step":"ModelGenerate","user_state":"DonePreview","pending_jobs":["j1"],
	  "settings":{"version":"v2","quality_override":500000},
	  "rodin_generate_info":{"mesh_input_images":{"0000.webp":"https://f/x?sig=1"},"mesh_reference_images":[],"material_input_images":null,
	    "confirmation_metadata":{"model_version":"v1","texture_superres":false}},
	  "card_poster_info":{"preview_image":"https://f/p.webp?sig=2","odd":5},"model_preview_info":[]}`
	card, err := ParseCard([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if card.PendingJobs != 1 || card.Setting("quality_override") != "500000" || card.Confirmed("model_version") != "v1" ||
		len(card.Info.MeshReferenceImages) != 0 || card.Poster["preview_image"] == "" || len(card.Poster) != 1 || len(card.Preview) != 0 {
		t.Fatalf("card %+v", card)
	}
}

func TestStripSignatures(t *testing.T) {
	out, err := StripSignatures([]byte(`{"a":"https://file.example/x/y.webp?sig=SECRET&e=1","b":[{"c":"http://h/z?q=1#f"}],"n":500000,"s":"plain?x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("SECRET")) || bytes.Contains(out, []byte("q=1")) || !bytes.Contains(out, []byte("https://file.example/x/y.webp")) ||
		!bytes.Contains(out, []byte("500000")) || !bytes.Contains(out, []byte("plain?x")) {
		t.Fatalf("stripped: %s", out)
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
}

func TestFetchSendsNoBearerAndHidesTheURL(t *testing.T) {
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(500)
			return
		}
		if r.URL.Path == "/missing" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte("hello"))
	})
	c := New(s.URL, &tokens{current: "t"}, "test")
	var buf bytes.Buffer
	n, _, err := c.Fetch(context.Background(), s.URL+"/f?sig=SECRET", &buf, 100)
	if err != nil || n != 5 || buf.String() != "hello" {
		t.Fatalf("n %d err %v body %q", n, err, buf.String())
	}
	_, _, err = c.Fetch(context.Background(), s.URL+"/missing?sig=SECRET", &buf, 100)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaks or is missing: %v", err)
	}
	_, _, err = c.Fetch(context.Background(), "http://127.0.0.1:1/x?sig=SECRET", &buf, 100)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("network error leaks or is missing: %v", err)
	}
	_, _, err = c.Fetch(context.Background(), s.URL+"/f?sig=SECRET", &buf, 3)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("limit: %v", err)
	}
}

func TestImageProcessIsMultipart(t *testing.T) {
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("generate_prompt") != "true" || r.MultipartForm.File["images"] == nil {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"prompt":"An owl","height":165,"allow_face_enhance":false,"num_multiview":1}`))
	})
	pp, err := New(s.URL, &tokens{current: "t"}, "test").ImageProcess(context.Background(), "a.png", "image/png", []byte("png"), true)
	if err != nil || pp.Prompt != "An owl" || pp.Height != "165" {
		t.Fatalf("pp %+v err %v", pp, err)
	}
}
