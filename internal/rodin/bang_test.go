package rodin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
)

// M2: each Bang call sends the body the web sends at its call sites:
// bang_plan and bang_plan_result add bang_version and bang_component_id
// only for one part (main.fmt.js:14416-14431, 92127-92136, 92269-92275),
// bang_generate sends only uuid and model_version (14559, 29989).
func TestBangBodiesAreTheWebs(t *testing.T) {
	var got []map[string]any
	var paths []string
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		got, paths = append(got, m), append(paths, r.URL.Path)
		w.WriteHeader(201)
		w.Write([]byte(`{"jobs":{"uuids":["j1"],"subscription_key":"k"},"version":"562c67","url":"https://files.example/plan.png"}`))
	})
	c := New(s.URL, &tokens{current: "t1"}, "test")
	ctx := context.Background()
	whole := BangRequest{UUID: "u", ModelVersion: "v2"}
	part := BangRequest{UUID: "u", ModelVersion: "v2", BangVersion: "562c67", ComponentID: "root.3"}
	if _, err := c.BangPlan(ctx, whole, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BangPlan(ctx, part, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BangPlanResult(ctx, whole); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BangPlanResult(ctx, part); err != nil {
		t.Fatal(err)
	}
	if r, err := c.BangGenerate(ctx, "u", "v2"); err != nil || r.Version != "562c67" || len(r.UUIDs()) != 1 {
		t.Fatalf("generate: %+v %v", r, err)
	}
	want := []map[string]any{
		{"uuid": "u", "model_version": "v2", "settings": map[string]any{"bang_component_num": 5.0}},
		{"uuid": "u", "model_version": "v2", "bang_version": "562c67", "bang_component_id": "root.3", "settings": map[string]any{"bang_component_num": 3.0}},
		{"uuid": "u", "model_version": "v2"},
		{"uuid": "u", "model_version": "v2", "bang_version": "562c67", "bang_component_id": "root.3"},
		{"uuid": "u", "model_version": "v2"},
	}
	wantPaths := []string{"/task/bang_plan", "/task/bang_plan", "/task/bang_plan_result", "/task/bang_plan_result", "/task/bang_generate"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths %v", paths)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("%s body %v, want %v", paths[i], got[i], want[i])
		}
	}
}
