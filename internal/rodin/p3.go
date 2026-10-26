package rodin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
)

// The procedures of the Rodin modes beyond the staged chain (notes 14
// sections 5-7, notes 49): Agentic, Bang (split into parts), rigging and
// animation packs. Like flow.go, nothing here is ever sent twice.

// Jobs is the jobs block of a mutating reply. Agentic and rigging answer
// "job", Bang answers "jobs"; UUIDs reads either.
type Jobs struct {
	Job struct {
		UUIDs []string `json:"uuids"`
	} `json:"job"`
	JobsBang struct {
		UUIDs []string `json:"uuids"`
	} `json:"jobs"`
}

// UUIDs returns the scheduler jobs of the reply.
func (j Jobs) UUIDs() []string {
	if len(j.Job.UUIDs) > 0 {
		return j.Job.UUIDs
	}
	return j.JobsBang.UUIDs
}

// AgenticRequest is the multipart body of task/rodin_agentic (builder at,
// main.fmt.js:146439).
type AgenticRequest struct {
	Prompt          string   // omitted when empty
	Height          *float64 // settings {height}; omitted when nil
	BudgetTier      string   // basic | standard | pro
	Route           string   // "rodin" forces the high-poly producer; empty omits it
	ClientRequestID string
	Images          []UploadImage
}

// AgenticReply is the reply of task/rodin_agentic: the new task and the
// producer the server chose (high-poly: an ordinary Rodin task; n-gons: a
// RodinAgentic task).
type AgenticReply struct {
	UUID     string `json:"uuid"`
	Producer string `json:"producer"`
	Jobs
}

// Agentic calls task/rodin_agentic: starts an Agentic task from images.
func (c *Client) Agentic(ctx context.Context, req AgenticRequest) (*AgenticReply, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	var fields [][2]string
	if req.Prompt != "" {
		fields = append(fields, [2]string{"prompt", req.Prompt})
	}
	if req.Height != nil {
		s, err := json.Marshal(map[string]float64{"height": *req.Height})
		if err != nil {
			return nil, err
		}
		fields = append(fields, [2]string{"settings", string(s)})
	}
	fields = append(fields, [2]string{"budget_tier", req.BudgetTier})
	if req.Route != "" {
		fields = append(fields, [2]string{"route", req.Route})
	}
	fields = append(fields, [2]string{"client_request_id", req.ClientRequestID})
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return nil, err
		}
	}
	for _, img := range req.Images {
		if err := writeFile(mw, "images", img); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	var out AgenticReply
	if err := c.callUpload(ctx, "task/rodin_agentic", body.Bytes(), mw.FormDataContentType(), &out); err != nil {
		return nil, err
	}
	if out.UUID == "" {
		return nil, &ServiceError{Procedure: "task/rodin_agentic", Code: "NO_TASK_IN_REPLY", Message: "the reply names no task"}
	}
	return &out, nil
}

// BangRequest names the geometry draft version (and, to split one part of
// an earlier Bang result, that result and the part) a Bang call works on.
type BangRequest struct {
	UUID         string
	ModelVersion string // geometry version, v1 ...
	BangVersion  string // the Bang result the part belongs to (with ComponentID)
	ComponentID  string // optional part name
}

// body is {uuid, model_version}, plus bang_version and bang_component_id
// for one part: the web's plan call and plan picture read (main.fmt.js:
// 14416-14431, 92127-92136, 92269-92275; the whole-model read sends no
// bang_component_id).
func (r BangRequest) body() map[string]any {
	b := map[string]any{"uuid": r.UUID, "model_version": r.ModelVersion}
	if r.ComponentID != "" {
		b["bang_version"], b["bang_component_id"] = r.BangVersion, r.ComponentID
	}
	return b
}

// BangReply is the reply of bang_plan and bang_generate.
type BangReply struct {
	Jobs
	Version string `json:"version"` // bang_generate: the new Bang version
}

// BangPlan calls task/bang_plan with the part count (the web's Strength,
// settings.bang_component_num 1-12).
func (c *Client) BangPlan(ctx context.Context, r BangRequest, components int) (*BangReply, error) {
	b := r.body()
	b["settings"] = map[string]int{"bang_component_num": components}
	var out BangReply
	if err := c.postJSON(ctx, "task/bang_plan", b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BangPlanResult calls task/bang_plan_result: the address of the plan's
// picture. No plan yet answers NO_SUCH_A_PLAN.
func (c *Client) BangPlanResult(ctx context.Context, r BangRequest) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := c.postJSON(ctx, "task/bang_plan_result", r.body(), &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// BangGenerate calls task/bang_generate: splits the draft by its latest
// plan (the web's "Bang to Parts"). Both web call sites send only uuid and
// model_version, also after a part's plan (main.fmt.js:14559, 29989).
func (c *Client) BangGenerate(ctx context.Context, uuid, modelVersion string) (*BangReply, error) {
	var out BangReply
	if err := c.postJSON(ctx, "task/bang_generate", map[string]any{"uuid": uuid, "model_version": modelVersion}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BangVersion is one entry of bang_history.
type BangVersion struct {
	GenerationTime string
	Model          string // signed address of the parts GLB
	Raw            map[string]any
}

// BangHistory calls task/bang_history for one geometry version: the Bang
// results by version.
func (c *Client) BangHistory(ctx context.Context, uuid, modelVersion string) (map[string]BangVersion, error) {
	var raw map[string]json.RawMessage
	if err := c.postJSON(ctx, "task/bang_history", map[string]string{"uuid": uuid, "model_version": modelVersion}, &raw); err != nil {
		return nil, err
	}
	out := map[string]BangVersion{}
	for v, entry := range raw {
		var m map[string]any
		if json.Unmarshal(entry, &m) != nil {
			continue // not a version entry
		}
		out[v] = BangVersion{GenerationTime: scalarText(m["generation_time"]), Model: scalarText(m["model"]), Raw: m}
	}
	return out, nil
}

// Rig sources: the confirmed geometry (also at the material draft) or the
// confirmed material (main.fmt.js:149431).
const (
	RigSourceModel   = "model_refine"
	RigSourceTexture = "texture_refine"
)

// Rigging calls task/rodin_rigging: generates a skeleton and skins the
// model. settings is the web's object (source, skinning_method,
// rodinrig_template and a hierarchy or a description).
func (c *Client) Rigging(ctx context.Context, uuid string, settings map[string]any) (*JobReply, error) {
	var out JobReply
	if err := c.postJSON(ctx, "task/rodin_rigging", map[string]any{"uuid": uuid, "settings": settings}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RigResult is the reply of rodin_rigging_result.
type RigResult struct {
	URL         string         `json:"url"`
	Template    string         `json:"rodinrig_template"`
	Description string         `json:"rodinrig_description"`
	Raw         map[string]any `json:"-"`
}

// RiggingResult calls task/rodin_rigging_result for a source stage. No
// rig answers RIGGING_NOT_FOUND.
func (c *Client) RiggingResult(ctx context.Context, uuid, source string) (*RigResult, error) {
	var raw json.RawMessage
	if err := c.postJSON(ctx, "task/rodin_rigging_result", map[string]any{"uuid": uuid, "source": map[string]string{"stage": source}}, &raw); err != nil {
		return nil, err
	}
	var out RigResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("task/rodin_rigging_result: decode reply: %w", err)
	}
	_ = json.Unmarshal(raw, &out.Raw)
	return &out, nil
}

// RigPackRequest is the multipart body of task/rodin_rigging_pack (builder
// fS, main.fmt.js:149355).
type RigPackRequest struct {
	TaskUUID    string
	Format      string // fbx | glb | usdz
	Resolution  string // Basic | High
	Material    string // All | PBR | Shaded | None
	Type        string // mesh | material
	Height      *float64
	AlignGround bool
	Presets     []string      // <category>/<name>
	Files       []UploadImage // own FBX animations
}

// RiggingPack calls task/rodin_rigging_pack: exports the rigged model with
// the animations. The reply is the job reply, as sent.
func (c *Client) RiggingPack(ctx context.Context, r RigPackRequest) (json.RawMessage, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fields := [][2]string{{"task_uuid", r.TaskUUID}, {"geometry_file_format", r.Format}, {"resolution", r.Resolution},
		{"material", r.Material}, {"type", r.Type}}
	if r.Height != nil {
		fields = append(fields, [2]string{"height", fmt.Sprint(*r.Height)})
	}
	fields = append(fields, [2]string{"align_ground", fmt.Sprint(r.AlignGround)}, [2]string{"rig_animation_trust_source", "false"})
	for _, p := range r.Presets {
		fields = append(fields, [2]string{"rodinrig_animations", p})
	}
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return nil, err
		}
	}
	for _, f := range r.Files {
		if err := writeFile(mw, "rodinrig_animation_files", f); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	var out json.RawMessage
	if err := c.callUpload(ctx, "task/rodin_rigging_pack", body.Bytes(), mw.FormDataContentType(), &out); err != nil {
		return nil, err
	}
	return out, nil
}
