package rodin

import (
	"encoding/json"
	"strconv"
)

// StringMap is a JSON object of strings that also accepts null, an empty
// array (the service sends [] for some empty maps) and non-string values,
// which it skips.
type StringMap map[string]string

func (m *StringMap) UnmarshalJSON(data []byte) error {
	*m = StringMap{}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil // null, [] or another shape: no entries
	}
	for k, v := range raw {
		var s string
		if json.Unmarshal(v, &s) == nil {
			(*m)[k] = s
		}
	}
	return nil
}

// Flex is a scalar that may arrive as a string, number or boolean; String
// renders it as text.
type Flex string

func (f *Flex) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		*f = Flex(s)
		return nil
	}
	var n float64
	if json.Unmarshal(data, &n) == nil {
		*f = Flex(strconv.FormatFloat(n, 'f', -1, 64))
		return nil
	}
	var b bool
	if json.Unmarshal(data, &b) == nil {
		*f = Flex(strconv.FormatBool(b))
		return nil
	}
	*f = ""
	return nil
}

func (f Flex) String() string { return string(f) }

// Count is the length of a JSON array that tolerates null and other shapes.
type Count int

func (c *Count) UnmarshalJSON(data []byte) error {
	var list []json.RawMessage
	if json.Unmarshal(data, &list) == nil {
		*c = Count(len(list))
		return nil
	}
	*c = 0
	return nil
}

// UserInfo is the reply of user/get_info.
type UserInfo struct {
	Meta struct {
		UserUUID         string  `json:"user_uuid"`
		Username         string  `json:"username"`
		Email            string  `json:"email"`
		Balance          float64 `json:"balance"`           // regular credits, tenths
		Frozen           float64 `json:"frozen"`            // tenths
		PrepaidAvailable float64 `json:"prepaid_available"` // regular + subscription, tenths
		Subscriptions    struct {
			CurrentActive bool   `json:"current_active"`
			HighestLevel  string `json:"highest_level"`
			Active        []struct {
				Active        bool    `json:"active"`
				PlanLevel     string  `json:"plan_level"`
				WalletBalance float64 `json:"wallet_balance"` // tenths
				StartTime     string  `json:"start_time"`
				EndTime       string  `json:"end_time"`
			} `json:"active_subscriptions"`
		} `json:"subscriptions"`
		Privileges []string `json:"privileges"`
		Features   []string `json:"features"`
	} `json:"meta"`
}

// HasPrivilege reports whether the account holds privilege name.
func (u *UserInfo) HasPrivilege(name string) bool {
	for _, p := range u.Meta.Privileges {
		if p == name {
			return true
		}
	}
	return false
}

// TaskRow is one row of task/list_Rodin.
type TaskRow struct {
	TaskUUID    string `json:"task_uuid"`
	Type        string `json:"type"`
	Prompt      string `json:"prompt"`
	Time        string `json:"time"`
	Step        string `json:"step"`
	IsPrivate   bool   `json:"is_private"`
	IsHidden    bool   `json:"is_hidden"`
	PendingJobs Count  `json:"pending_jobs"`
}

// TaskPage is one page of task/list_Rodin.
type TaskPage struct {
	Pagination struct {
		Total      int `json:"total"`
		Page       int `json:"page"`
		Limit      int `json:"limit"`
		TotalPages int `json:"total_pages"`
	} `json:"pagination"`
	Data []TaskRow `json:"data"`
}

// TypeAgentic is the card type of a task made by the n-gons producer of
// task/rodin_agentic (main.fmt.js:10736-10790); the high-poly producer
// makes an ordinary "Rodin" task.
const TypeAgentic = "RodinAgentic"

// Card is the reply of task/card/<uuid>: the whole state of one task. Raw
// keeps the reply as sent, so fields this type does not name are not lost.
type Card struct {
	TaskUUID     string         `json:"task_uuid"`
	Type         string         `json:"type"`
	Prompt       string         `json:"prompt"`
	Time         string         `json:"time"`
	GenerateTime string         `json:"generate_time"`
	Step         string         `json:"step"`
	UserState    string         `json:"user_state"`
	IsPrivate    bool           `json:"is_private"`
	PendingJobs  Count          `json:"pending_jobs"`
	Settings     map[string]any `json:"settings"`
	Info         struct {
		MeshInputImages     StringMap      `json:"mesh_input_images"`
		MeshReferenceImages StringMap      `json:"mesh_reference_images"`
		MaterialInputImages StringMap      `json:"material_input_images"`
		IsBang              bool           `json:"is_bang"`
		Confirmation        map[string]any `json:"confirmation_metadata"`
	} `json:"rodin_generate_info"`
	Poster  StringMap       `json:"card_poster_info"`
	Preview StringMap       `json:"model_preview_info"`
	Raw     json.RawMessage `json:"-"`
}

// Setting returns settings[key] as text ("" when absent or not a scalar).
func (c *Card) Setting(key string) string {
	return scalarText(c.Settings[key])
}

// Confirmed returns confirmation_metadata[key] as text.
func (c *Card) Confirmed(key string) string {
	return scalarText(c.Info.Confirmation[key])
}

func scalarText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

// GeometryVersion is one entry of rodin_history for step ModelGenerate.
type GeometryVersion struct {
	PreviewCover   string `json:"preview_cover"`
	PreviewDRC     string `json:"preview_drc"`
	PreviewImage   string `json:"preview_image"` // Gen-1.5: the turntable sheet (render.jpg), no preview_drc
	Settings       string `json:"settings"`
	GenerationTime string `json:"generation_time"`
	HasBang        bool   `json:"has_bang"`
}

// MaterialVersion is one entry of rodin_history for step TextureGenerate.
type MaterialVersion struct {
	PreviewImage   string `json:"preview_image"`
	TextureDiffuse string `json:"texture_diffuse"`
	TextureNormal  string `json:"texture_normal"`
	TexturePBR     string `json:"texture_pbr"`
	TextureShaded  string `json:"texture_shaded"`
	Model          string `json:"model"`
	Settings       string `json:"settings"`
}

// DownloadItem is one file of a finished pack.
type DownloadItem struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Preprocess is the reply of task/rodin_mesh_image_process.
type Preprocess struct {
	Prompt             string `json:"prompt"`
	Height             Flex   `json:"height"`
	AllowFaceEnhance   bool   `json:"allow_face_enhance"`
	DefaultFaceEnhance bool   `json:"default_face_enhance"`
	IsMultiview        bool   `json:"is_multiview"`
	NumMultiview       Flex   `json:"num_multiview"`
}
