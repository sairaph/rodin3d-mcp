package rodin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
)

// The staged Rodin chain (notes 14 sections 2-4, bundle API module):
// rodin_mesh (draft) -> rodin_update (redo) -> rodin_confirm or rodin_polygen
// (geometry) -> rodin_texture (material draft) -> rodin_update (material
// redo) -> rodin_confirm stage material -> rodin_unlock (pack), plus
// rodin_fork, select_candidate and set_private. Every one of them spends credits or
// changes a task, so none is ever sent twice by this package: a 401 or a
// body-level session refusal (the request was not processed) gets the one
// refresh and resend of call, a network failure is returned as it is.

// UploadImage is one image file sent with a submit.
type UploadImage struct {
	Name string // file name the server sees
	MIME string // image/jpeg, image/png or image/webp
	Data []byte
}

// JobReply is what the mutating procedures answer: the task (new for a
// submit or a fork) and the scheduler jobs started. The reply's
// subscription_key (a signed token for the job socket) is not decoded.
type JobReply struct {
	UUID string `json:"uuid"`
	Job  struct {
		UUIDs []string `json:"uuids"`
	} `json:"job"`
	// ReferenceFilename is the material reference rodin_texture used.
	ReferenceFilename string `json:"reference_filename"`
}

// MeshRequest is the multipart body of task/rodin_mesh (builder Je,
// main@5357283).
type MeshRequest struct {
	Prompt           string
	Settings         map[string]any
	UseOriginalAlpha []bool
	BatchGen         int // 0 or 1: no batch_gen field
	Images           []UploadImage
}

// Mesh calls task/rodin_mesh: submits a new image-to-3D task. The reply
// carries the new task uuid.
func (c *Client) Mesh(ctx context.Context, req MeshRequest) (*JobReply, error) {
	settings, err := json.Marshal(req.Settings)
	if err != nil {
		return nil, fmt.Errorf("task/rodin_mesh: encode settings: %w", err)
	}
	alpha, err := json.Marshal(req.UseOriginalAlpha)
	if err != nil {
		return nil, fmt.Errorf("task/rodin_mesh: encode use_original_alpha: %w", err)
	}
	prompt := req.Prompt
	if prompt == "" {
		prompt = " " // the web sends a blank, never an empty field
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fields := [][2]string{{"prompt", prompt}, {"settings", string(settings)}, {"use_original_alpha", string(alpha)}}
	if req.BatchGen > 1 {
		fields = append(fields, [2]string{"batch_gen", strconv.Itoa(req.BatchGen)})
	}
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
	var out JobReply
	if err := c.callUpload(ctx, "task/rodin_mesh", body.Bytes(), mw.FormDataContentType(), &out); err != nil {
		return nil, err
	}
	if out.UUID == "" {
		return nil, &ServiceError{Procedure: "task/rodin_mesh", Code: "NO_TASK_IN_REPLY", Message: "the reply names no task"}
	}
	return &out, nil
}

// Update calls task/rodin_update: a redo of the geometry draft or of the
// material draft. referenceImage is omitted when empty (a geometry redo
// sends none; a material redo sends the material reference).
func (c *Client) Update(ctx context.Context, uuid, prompt, referenceImage string, settings map[string]any) (*JobReply, error) {
	if prompt == "" {
		prompt = " "
	}
	body := map[string]any{"uuid": uuid, "prompt": prompt, "settings": settings}
	if referenceImage != "" {
		body["reference_image"] = referenceImage
	}
	var out JobReply
	if err := c.postJSON(ctx, "task/rodin_update", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Confirm calls task/rodin_confirm (geometry or, with stage material in the
// settings shape, material), or task/rodin_polygen when polygen is true
// (smart low-poly geometry confirm). body is the web's object as built by
// the caller.
func (c *Client) Confirm(ctx context.Context, body map[string]any, polygen bool) (*JobReply, error) {
	proc := "task/rodin_confirm"
	if polygen {
		proc = "task/rodin_polygen"
	}
	var out JobReply
	if err := c.postJSON(ctx, proc, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TextureRequest is the multipart body of task/rodin_texture (mt,
// main@5364762): either a reference already on the task (ReferenceFilename,
// an input image name or "ALL") or a new reference image.
type TextureRequest struct {
	UUID              string
	Prompt            string
	ReferenceFilename string
	NewReference      *UploadImage
	Settings          map[string]any
}

// Texture calls task/rodin_texture: starts the material draft.
func (c *Client) Texture(ctx context.Context, req TextureRequest) (*JobReply, error) {
	settings, err := json.Marshal(req.Settings)
	if err != nil {
		return nil, fmt.Errorf("task/rodin_texture: encode settings: %w", err)
	}
	prompt := req.Prompt
	if prompt == "" {
		prompt = " "
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("uuid", req.UUID); err != nil {
		return nil, err
	}
	if req.NewReference != nil {
		if err := writeFile(mw, "images", *req.NewReference); err != nil {
			return nil, err
		}
		if err := mw.WriteField("texture_reference_file_index", "0"); err != nil {
			return nil, err
		}
	} else if err := mw.WriteField("texture_reference_filename", req.ReferenceFilename); err != nil {
		return nil, err
	}
	for _, f := range [][2]string{{"prompt", prompt}, {"settings", string(settings)}} {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	var out JobReply
	if err := c.callUpload(ctx, "task/rodin_texture", body.Bytes(), mw.FormDataContentType(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Unlock calls task/rodin_unlock: packs the task's files for download
// (builder Tt, main@5365649). body is the web's object as built by the
// caller.
func (c *Client) Unlock(ctx context.Context, body map[string]any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := c.postJSON(ctx, "task/rodin_unlock", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Fork calls task/rodin_fork: a new task branched from state
// (StepModelGenerate or StepTextureGenerate) of uuid. It returns the new
// task's uuid.
func (c *Client) Fork(ctx context.Context, uuid, state string) (string, error) {
	var out struct {
		UUID string `json:"uuid"`
	}
	if err := c.postJSON(ctx, "task/rodin_fork", map[string]string{"uuid": uuid, "state": state}, &out); err != nil {
		return "", err
	}
	if out.UUID == "" {
		return "", &ServiceError{Procedure: "task/rodin_fork", Code: "NO_TASK_IN_REPLY", Message: "the reply names no task"}
	}
	return out.UUID, nil
}

// SelectCandidate calls task/select_candidate: picks one draft of a batch.
// selectedID is a number (the server refuses a string: "selected_id must be
// a number", live 2026-10-26); the candidates are the draft's versions. A
// success answers with plain text, not JSON (live), so the body is only
// checked for an error code.
func (c *Client) SelectCandidate(ctx context.Context, uuid string, selectedID int) error {
	return c.postJSON(ctx, "task/select_candidate", map[string]any{"uuid": uuid, "selected_id": selectedID}, nil)
}

// SetPrivate calls task/set_private {task_uuid}: it flips the task's
// privacy. The web sends the same call to make a task private and to make it
// public (privacy button, main@fmt:82830) and right after a submit when its
// "private" switch is on (main@fmt:86906); rodin_mesh and rodin_fork carry no
// privacy field. Live 2026-10-26: HTTP 201 with {} and the card's is_private
// turned true at once. A plan without UnlimitedPrivateModel has a quota
// (user/get_info chances_left_to_set_private_task) and is refused with
// INSUFFICIENT_CHANCES. Since a second call flips the task back, callers read
// the card instead of resending.
func (c *Client) SetPrivate(ctx context.Context, uuid string) error {
	return c.postJSON(ctx, "task/set_private", map[string]string{"task_uuid": uuid}, nil)
}

// JobStatus calls GET scheduler?uuid=<job>: Queued, Running, Succeeded,
// Failed, Canceled or BackPressure (notes 14 section 3.4). An unknown job
// reads as Canceled.
func (c *Client) JobStatus(ctx context.Context, job string) (string, error) {
	var out struct {
		Status string `json:"status"`
	}
	q := url.Values{"uuid": {job}}
	if err := c.call(ctx, request{method: http.MethodGet, procedure: "scheduler", query: q}, &out); err != nil {
		return "", err
	}
	return out.Status, nil
}

// callUpload is call for a multipart body that may take longer than an API
// call: it uses the upload client (file timeout, same redirect rule).
func (c *Client) callUpload(ctx context.Context, procedure string, body []byte, contentType string, out any) error {
	return c.call(ctx, request{method: http.MethodPost, procedure: procedure, body: body, contentType: contentType, upload: true}, out)
}

func writeFile(mw *multipart.Writer, field string, img UploadImage) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, img.Name))
	h.Set("Content-Type", img.MIME)
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = part.Write(img.Data)
	return err
}
