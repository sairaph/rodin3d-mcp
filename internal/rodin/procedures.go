package rodin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
)

// Steps a Rodin task passes through; list_Rodin accepts exactly these four.
const (
	StepModelGenerate   = "ModelGenerate"
	StepModelRefine     = "ModelRefine"
	StepTextureGenerate = "TextureGenerate"
	StepTextureRefine   = "TextureRefine"
)

// ListSteps is the step filter the web sends with every list_Rodin call.
var ListSteps = []string{StepModelGenerate, StepModelRefine, StepTextureGenerate, StepTextureRefine}

// GetInfo calls user/get_info: account, wallet, plan, privileges.
func (c *Client) GetInfo(ctx context.Context) (*UserInfo, error) {
	var out UserInfo
	if err := c.postJSON(ctx, "user/get_info", struct{}{}, &out); err != nil {
		return nil, err
	}
	if out.Meta.UserUUID == "" {
		return nil, &ServiceError{Procedure: "user/get_info", Code: "NO_CURRENT_USER", Message: "the reply carries no user"}
	}
	return &out, nil
}

// ListMine calls task/list_Rodin/Mine for one page (page counts from 1).
func (c *Client) ListMine(ctx context.Context, userUUID string, page, limit int) (*TaskPage, error) {
	q := url.Values{}
	if userUUID != "" {
		q.Set("user_uuid", userUUID)
	}
	q.Set("page", strconv.Itoa(page))
	q.Set("limit", strconv.Itoa(limit))
	for _, s := range ListSteps {
		q.Add("step", s)
	}
	q.Set("hidden", "false")
	var out TaskPage
	if err := c.call(ctx, request{method: http.MethodGet, procedure: "task/list_Rodin/Mine", query: q}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Card calls task/card/<uuid>.
func (c *Client) Card(ctx context.Context, uuid string) (*Card, error) {
	var raw json.RawMessage
	if err := c.postJSON(ctx, "task/card/"+url.PathEscape(uuid), struct{}{}, &raw); err != nil {
		return nil, err
	}
	return ParseCard(raw)
}

// ParseCard decodes a card reply, keeping the raw bytes.
func ParseCard(raw []byte) (*Card, error) {
	var card Card
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, fmt.Errorf("task/card: decode reply: %w", err)
	}
	if card.TaskUUID == "" {
		return nil, &ServiceError{Procedure: "task/card", Code: "NO_SUCH_TASK"}
	}
	card.Raw = append(json.RawMessage(nil), raw...)
	return &card, nil
}

// CheckProgress calls task/check_progress/<uuid> and returns its stage:
// Waiting, Generating or Done.
func (c *Client) CheckProgress(ctx context.Context, uuid string) (string, error) {
	var out struct {
		Stage string `json:"stage"`
	}
	err := c.call(ctx, request{method: http.MethodPost, procedure: "task/check_progress/" + url.PathEscape(uuid), body: []byte{}}, &out)
	return out.Stage, err
}

// GeometryHistory calls task/rodin_history for ModelGenerate: one entry per
// geometry draft version (v1, v2, ...). No history is an empty map.
func (c *Client) GeometryHistory(ctx context.Context, uuid string) (map[string]GeometryVersion, error) {
	var raw map[string]json.RawMessage
	if err := c.history(ctx, uuid, StepModelGenerate, &raw); err != nil {
		return nil, err
	}
	out := map[string]GeometryVersion{}
	for version, entry := range raw {
		var m map[string]any
		if json.Unmarshal(entry, &m) != nil {
			continue
		}
		hasBang, _ := m["has_bang"].(bool)
		out[version] = GeometryVersion{
			PreviewCover: scalarText(m["preview_cover"]), PreviewDRC: scalarText(m["preview_drc"]),
			Settings: scalarText(m["settings"]), GenerationTime: scalarText(m["generation_time"]), HasBang: hasBang,
		}
	}
	return out, nil
}

// MaterialHistory calls task/rodin_history for TextureGenerate: per
// reference ("ALL" or an input image name), one entry per material version.
func (c *Client) MaterialHistory(ctx context.Context, uuid string) (map[string]map[string]MaterialVersion, error) {
	var raw map[string]map[string]json.RawMessage
	if err := c.history(ctx, uuid, StepTextureGenerate, &raw); err != nil {
		return nil, err
	}
	out := map[string]map[string]MaterialVersion{}
	for ref, versions := range raw {
		out[ref] = map[string]MaterialVersion{}
		for version, entry := range versions {
			var m map[string]any
			if json.Unmarshal(entry, &m) != nil {
				continue
			}
			out[ref][version] = MaterialVersion{
				PreviewImage: scalarText(m["preview_image"]), TextureDiffuse: scalarText(m["texture_diffuse"]),
				TextureNormal: scalarText(m["texture_normal"]), TexturePBR: scalarText(m["texture_pbr"]),
				TextureShaded: scalarText(m["texture_shaded"]), Model: scalarText(m["model"]), Settings: scalarText(m["settings"]),
			}
		}
	}
	return out, nil
}

func (c *Client) history(ctx context.Context, uuid, step string, out any) error {
	err := c.postJSON(ctx, "task/rodin_history", map[string]string{"uuid": uuid, "step": step}, out)
	var se *ServiceError
	if errors.As(err, &se) && se.Code == "NO_HISTORY_AVAILABLE" {
		return nil
	}
	return err
}

// GetDownload calls task/get_download: the files of the finished pack. A task
// that has no pack yet answers INVALID_REQUEST (a ServiceError).
func (c *Client) GetDownload(ctx context.Context, uuid string) ([]DownloadItem, error) {
	var out struct {
		List []DownloadItem `json:"list"`
	}
	if err := c.postJSON(ctx, "task/get_download", map[string]string{"task_uuid": uuid}, &out); err != nil {
		return nil, err
	}
	return out.List, nil
}

// ImageProcess calls task/rodin_mesh_image_process for one image (free): the
// server's caption, height and face-enhance flags. generatePrompt is true for
// the first image of a submit only.
func (c *Client) ImageProcess(ctx context.Context, filename, mimeType string, data []byte, generatePrompt bool) (*Preprocess, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("generate_prompt", strconv.FormatBool(generatePrompt)); err != nil {
		return nil, err
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="images"; filename=%q`, filename))
	h.Set("Content-Type", mimeType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	var out Preprocess
	err = c.call(ctx, request{method: http.MethodPost, procedure: "task/rodin_mesh_image_process", body: body.Bytes(), contentType: mw.FormDataContentType()}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// FetchPrefix starts the procedure name of every Fetch error ("fetch from
// <host>").
const FetchPrefix = "fetch from "

// ErrTooLarge means a fetched file passed the size limit given to Fetch.
var ErrTooLarge = errors.New("file larger than the limit")

// Fetch downloads a signed file URL into w, without the bearer token. It
// returns the bytes written and the reply's content type. Errors never carry
// the URL, whose query is the signature; they name the host only.
func (c *Client) Fetch(ctx context.Context, signedURL string, w io.Writer, limit int64) (int64, string, error) {
	r, err := c.FetchIf(ctx, signedURL, w, limit, "")
	return r.Bytes, r.ContentType, err
}

// FetchResult is what FetchIf learned.
type FetchResult struct {
	Bytes       int64
	ContentType string
	// Validator identifies the file version: its ETag, else its
	// Last-Modified prefixed with "lm:"; empty when the host sends neither.
	Validator   string
	NotModified bool // the validator still matches: nothing was written
}

// FetchIf is Fetch with a conditional request: given the validator of a
// stored copy, a host that still holds the same file answers 304 and
// nothing is written.
func (c *Client) FetchIf(ctx context.Context, signedURL string, w io.Writer, limit int64, validator string) (FetchResult, error) {
	var res FetchResult
	u, err := url.Parse(signedURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return res, errors.New("fetch: not an http(s) file address")
	}
	host := u.Host
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signedURL, nil)
	if err != nil {
		return res, fmt.Errorf("%s%s: build request", FetchPrefix, host)
	}
	req.Header.Set("User-Agent", c.userAgent)
	switch {
	case strings.HasPrefix(validator, "lm:"):
		req.Header.Set("If-Modified-Since", strings.TrimPrefix(validator, "lm:"))
	case validator != "":
		req.Header.Set("If-None-Match", validator)
	}
	resp, err := c.files.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return res, &NetError{Procedure: FetchPrefix + host, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && validator != "" {
		res.NotModified, res.Validator = true, validator
		return res, nil
	}
	if resp.StatusCode != http.StatusOK {
		return res, &HTTPError{Procedure: FetchPrefix + host, Status: resp.StatusCode}
	}
	if et := resp.Header.Get("ETag"); et != "" {
		res.Validator = et
	} else if lm := resp.Header.Get("Last-Modified"); lm != "" {
		res.Validator = "lm:" + lm
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	res.Bytes = n
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return res, &NetError{Procedure: FetchPrefix + host, Err: err}
	}
	if n > limit {
		return res, fmt.Errorf("%s%s: %w (%d bytes)", FetchPrefix, host, ErrTooLarge, limit)
	}
	res.ContentType = resp.Header.Get("Content-Type")
	return res, nil
}

// StripSignatures returns raw JSON with the query and fragment removed from
// every http(s) address in it, so a stored card holds no signed URL.
func StripSignatures(raw []byte) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(strip(v))
}

func strip(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = strip(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = strip(e)
		}
		return x
	case string:
		if strings.HasPrefix(x, "https://") || strings.HasPrefix(x, "http://") {
			if u, err := url.Parse(x); err == nil {
				u.RawQuery, u.Fragment, u.User = "", "", nil
				return u.String()
			}
			return ""
		}
	}
	return v
}

// SourceKey identifies the file behind a signed URL without its signature:
// host and path. Stable across re-signing, so a cache can tell an unchanged
// file from a new one.
func SourceKey(signedURL string) string {
	u, err := url.Parse(signedURL)
	if err != nil {
		return ""
	}
	return u.Host + u.Path
}
