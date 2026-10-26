package mcpserver

import (
	"context"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/price"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
)

func ptr[T any](v T) *T { return &v }

// Annotation sets.
var (
	readTool  = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)}
	localTool = &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(true)}
)

func addTool[In any](s *Server, name, title, description string, ann *mcp.ToolAnnotations, schema *jsonschema.Schema,
	h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error)) {
	rememberArguments(name, schema)
	a := *ann
	a.Title = title
	mcp.AddTool(s.mcpServer, &mcp.Tool{Name: name, Title: title, Description: description, Annotations: &a, InputSchema: schema}, recovering(name, h))
}

func optString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func optInt(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func optBool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

type noInput struct{}

type loginInput struct {
	RedirectURL *string `json:"redirect_url,omitempty" jsonschema:"the final address the browser showed after Authorize (http://127.0.0.1:5368x/callback?code=...), pasted by the user; omit to start a new sign-in"`
	OpenBrowser *bool   `json:"open_browser,omitempty" jsonschema:"open the sign-in link in this computer's default browser too (default true); the link is returned either way"`
}

type optionsInput struct {
	Model *string `json:"model,omitempty" jsonschema:"limit the catalogue to one model"`
}

type quoteSettings struct {
	Model        *string `json:"model,omitempty" jsonschema:"generate_model: gen-2.5 (default) or gen-1.5"`
	Tier         *string `json:"tier,omitempty" jsonschema:"generate_model and export_model: the Gen-2.5 tier (taken from the task when task_id is given)"`
	TextureMode  *string `json:"texture_mode,omitempty" jsonschema:"generate_texture: the texture tier (default high, the web default)"`
	SmartLowPoly *bool   `json:"smart_low_poly,omitempty" jsonschema:"confirm_geometry: confirm in smart low-poly mode"`
	Resolution   *string `json:"resolution,omitempty" jsonschema:"export_model: pack texture resolution"`
	Geometry     *string `json:"geometry,omitempty" jsonschema:"export_model: pack geometry"`
	Budget       *string `json:"budget,omitempty" jsonschema:"generate_agentic: the Agentic budget"`
	Batch        *int    `json:"batch,omitempty" jsonschema:"generate_model: drafts per submit (more than 1 needs the Business plan)"`
}

type quoteInput struct {
	Operation string         `json:"operation" jsonschema:"the operation to price"`
	TaskID    *string        `json:"task_id,omitempty" jsonschema:"the task the operation acts on; its tier and age feed the quote"`
	Settings  *quoteSettings `json:"settings,omitempty" jsonschema:"the settings the price depends on"`
}

type listInput struct {
	Stage *string `json:"stage,omitempty" jsonschema:"only tasks at this stage"`
	State *string `json:"state,omitempty" jsonschema:"only tasks in this state"`
	Query *string `json:"query,omitempty" jsonschema:"text the prompt contains, or the start of a task id"`
	Page  *int    `json:"page,omitempty" jsonschema:"page number, from 1"`
}

type taskInput struct {
	TaskID string `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
}

type waitInput struct {
	TaskID         string `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	TimeoutSeconds *int   `json:"timeout_seconds,omitempty" jsonschema:"how long to wait for a change, 1-50 seconds"`
}

type previewInput struct {
	TaskID string  `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	Stage  *string `json:"stage,omitempty" jsonschema:"which stage's images: current (the task's own stage), geometry_draft, geometry (confirmed) or material"`
	Mode   *string `json:"mode,omitempty" jsonschema:"which image: poster (the stage render), geometry, material, or a texture map (diffuse, pbr, normal, shaded)"`
	Size   *int    `json:"size,omitempty" jsonschema:"longer edge of the PNG in pixels, 256-1024"`
	SaveTo *string `json:"save_to,omitempty" jsonschema:"absolute path of the PNG to write; default the previews folder of the data directory"`
}

type downloadInput struct {
	TaskID    string   `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	OutputDir *string  `json:"output_dir,omitempty" jsonschema:"absolute folder to save into; default downloads/<id8>-<prompt> in the data directory"`
	Files     []string `json:"files,omitempty" jsonschema:"names to save, from get_task files.pack or files.videos; default the whole pack"`
}

func (s *Server) registerTools() {
	addTool(s, "get_account", "Account and wallet",
		"Show the signed-in Hyper3D account: username, plan and renewal date, the wallet in credits (available, regular, subscription, frozen), "+
			"the [permission] values of config.toml that allow or deny each paid or changing operation, the session's state (login.expires_at "+
			"is when the current access token expires; it is refreshed on its own, so it is not a sign-in deadline), and the background "+
			"syncer's state and last sync. Call it first. When it answers authentication, call login and give the user the link.",
		readTool, inputSchema[noInput](nil), s.getAccount)

	addTool(s, "login", "Sign in",
		"Sign in to Hyper3D with this tool's own OAuth client. Without redirect_url it starts a sign-in and returns authorize_url plus a QR "+
			"code of it: the user opens it on any device, signs in to Hyper3D if asked and clicks Authorize. When that browser runs on this "+
			"computer the sign-in finishes by itself; otherwise the browser ends on a page that cannot connect to 127.0.0.1, and the user pastes "+
			"that page's whole address back: call login again with it as redirect_url. Then call get_account. A link works for 10 minutes; "+
			"a new sign-in replaces the current session.",
		localTool, nonNull(inputSchema[loginInput](map[string]string{"open_browser": "true"})), s.login)

	addTool(s, "logout", "Sign out",
		"Sign out of Hyper3D: revoke this tool's grant on the server, delete the tokens on this computer and stop the background syncer. "+
			"The local task index and downloaded files stay. Call login to sign in again.",
		&mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(true)},
		inputSchema[noInput](nil), s.logout)

	addTool(s, "get_generation_options", "Generation settings",
		"Describe everything a Rodin image-to-3D generation can be told: the models (Gen-2.5 tiers, Gen-1.5 modes), every setting of each "+
			"stage (generate, confirm geometry, texture, export) with its web field, values, default and plan gate, the image view tags and "+
			"default weights for multi-view input, the server image modes get_preview offers per stage, which privileges this account's plan "+
			"has, the operations quote_cost prices with their permission keys, and the recipes found in the account's own history (setting "+
			"combinations with example task ids to open with get_task).",
		readTool, withEnum(inputSchema[optionsInput](nil), "model", "gen-2.5", "gen-1.5"), s.getOptions)

	quote := inputSchema[quoteInput](nil)
	ops := make([]string, len(price.Operations))
	for i, o := range price.Operations {
		ops[i] = o.Name
	}
	withEnum(quote, "operation", ops...)
	settings := property(quote, "settings")
	dropNullAlt(settings)
	nonNull(settings)
	withEnum(settings, "model", "gen-2.5", "gen-1.5")
	withEnum(settings, "tier", "extreme-low", "low", "medium", "high", "extreme-high")
	withEnum(settings, "texture_mode", "extreme-low", "low", "medium", "high", "extreme-high")
	withEnum(settings, "resolution", "1k", "2k", "4k", "6k", "12k")
	withEnum(settings, "geometry", "base", "lod", "high_poly")
	withEnum(settings, "budget", "basic", "standard", "pro")
	withRange(settings, 1, 10, "batch")
	addTool(s, "quote_cost", "Price of an operation",
		"Quote what an operation costs before it is run: credits, when they are taken, why it may be free, and where the number comes from "+
			"(the web app shows fixed price labels and the server freezes the real amount; operations without a label say the price is not "+
			"known). Also returns the wallet, whether the account can afford it, and the operation's permission key and value in config.toml. "+
			"Pass task_id to price an operation on that task (its tier and age are read from it). Answers forbidden when the account's plan "+
			"does not allow the operation or option.",
		readTool, nonNull(quote), s.quoteCost)

	list := nonNull(inputSchema[listInput](nil))
	withEnum(list, "stage", stage.GeometryDraft, stage.Geometry, stage.MaterialDraft, stage.Material)
	withEnum(list, "state", stage.Running, stage.Ready, stage.Packed)
	withMin(list, 1, "page")
	addTool(s, "list_tasks", "List generations",
		"List the account's Rodin tasks from the local index the background syncer keeps (every 15 minutes, and every 5 seconds for tasks "+
			"that are running), newest first: prompt, id, stage (geometry_draft, geometry, material_draft, material), state (running, ready, "+
			"packed), model and tier, view tags, creation time. Filter by stage, state, or query (prompt text or id start); results come in "+
			"pages sized to fit the reply. Needs a signed-in account (the index is shown only to the account it belongs to); right after the "+
			"first login it waits for the first sync. Use get_task for one task's details.",
		readTool, list, s.listTasks)

	addTool(s, "get_task", "Task details",
		"Read one task live from Hyper3D: its stage and state with the actions it allows, every stored setting grouped by stage (geometry, "+
			"confirm, material, pack), the input images with their view tags and weights, geometry and material versions with the confirmed "+
			"ones, the free redos left at a draft, the preview images available per stage (for get_preview), the packed files (for "+
			"download_model) and the files already downloaded. The current stage's poster is attached as an image.",
		readTool, inputSchema[taskInput](nil), s.getTask)

	wait := nonNull(inputSchema[waitInput](map[string]string{"timeout_seconds": "45"}))
	withRange(wait, 1, 50, "timeout_seconds")
	addTool(s, "wait_task", "Wait for a task",
		"Wait while a job works on a task: the card is read every 5 seconds until the stage or state changes or timeout_seconds pass. "+
			"Returns the task, changed and timed_out; when it changed to a finished stage, its new poster is attached. A task with no running "+
			"job answers at once. After a time-out, call wait_task again.",
		readTool, wait, s.waitTask)

	prev := nonNull(inputSchema[previewInput](map[string]string{"stage": `"current"`, "mode": `"poster"`, "size": "512"}))
	withEnum(prev, "stage", "current", assets.StageGeometryDraft, assets.StageGeometry, assets.StageMaterial)
	withEnum(prev, "mode", assets.AllModes...)
	withRange(prev, service.PreviewMin, service.PreviewMax, "size")
	addTool(s, "get_preview", "Preview image",
		"Show one server image of a task as a PNG: the poster of a stage (geometry draft, confirmed geometry, material), the geometry and "+
			"material renders, or a texture map (diffuse, pbr, normal, shaded) once textured. Earlier stages of a later task work too "+
			"(stage geometry_draft shows the draft that was confirmed). The image is attached and saved to save_to or the previews folder. "+
			"A mode the stage does not have answers conflict with the available stage and mode pairs; get_task lists them as previews.",
		readTool, prev, s.getPreview)

	addTool(s, "download_model", "Download model files",
		"Save the files of a task's finished pack (the model in its packed format, textures) to output_dir or downloads/<id8>-<prompt> in "+
			"the data directory; files limits the download to names from get_task files.pack, and the stage videos (files.videos) can be "+
			"named too. Answers conflict when the task has no finished pack yet. Returns each file's path and size.",
		localTool, nonNull(inputSchema[downloadInput](nil)), s.downloadModel)

	s.registerFlowTools()
}

// --- handlers ---

func (s *Server) getAccount(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.Account(ctx)), nil, nil
}

func (s *Server) login(ctx context.Context, _ *mcp.CallToolRequest, in loginInput) (*mcp.CallToolResult, any, error) {
	res, started, err := s.svc.Login(ctx, service.LoginInput{RedirectURL: optString(in.RedirectURL), OpenBrowser: optBool(in.OpenBrowser, true)})
	if err == nil && started != nil && started.Done != nil {
		go func() {
			if o := <-started.Done; o.Err == nil {
				s.svc.AfterLogin(context.Background(), o.Credentials)
			}
		}()
	}
	return reply(res, err), nil, nil
}

func (s *Server) logout(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.Logout(ctx)), nil, nil
}

func (s *Server) getOptions(ctx context.Context, _ *mcp.CallToolRequest, in optionsInput) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.Options(ctx, optString(in.Model))), nil, nil
}

func (s *Server) quoteCost(ctx context.Context, _ *mcp.CallToolRequest, in quoteInput) (*mcp.CallToolResult, any, error) {
	q := service.QuoteInput{Operation: in.Operation, TaskID: optString(in.TaskID)}
	if st := in.Settings; st != nil {
		q.Model, q.Tier, q.TextureMode = optString(st.Model), optString(st.Tier), optString(st.TextureMode)
		q.SmartLowPoly = optBool(st.SmartLowPoly, false)
		q.Resolution, q.Geometry, q.Budget = optString(st.Resolution), optString(st.Geometry), optString(st.Budget)
		q.Batch = optInt(st.Batch, 0)
	}
	return reply(s.svc.Quote(ctx, q)), nil, nil
}

func (s *Server) listTasks(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.ListTasks(ctx, service.ListInput{Stage: optString(in.Stage), State: optString(in.State), Query: optString(in.Query), Page: optInt(in.Page, 1)})), nil, nil
}

func (s *Server) getTask(ctx context.Context, _ *mcp.CallToolRequest, in taskInput) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.GetTask(ctx, in.TaskID)), nil, nil
}

func (s *Server) waitTask(ctx context.Context, _ *mcp.CallToolRequest, in waitInput) (*mcp.CallToolResult, any, error) {
	timeout := time.Duration(optInt(in.TimeoutSeconds, 45)) * time.Second
	return reply(s.svc.WaitTask(ctx, in.TaskID, timeout)), nil, nil
}

func (s *Server) getPreview(ctx context.Context, _ *mcp.CallToolRequest, in previewInput) (*mcp.CallToolResult, any, error) {
	return imageReply(s.svc.Preview(ctx, service.PreviewInput{
		TaskID: in.TaskID, Stage: optString(in.Stage), Mode: optString(in.Mode), Size: optInt(in.Size, service.PreviewDefault), SaveTo: optString(in.SaveTo),
	})), nil, nil
}

func (s *Server) downloadModel(ctx context.Context, _ *mcp.CallToolRequest, in downloadInput) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.Download(ctx, service.DownloadInput{TaskID: in.TaskID, OutputDir: optString(in.OutputDir), Files: in.Files})), nil, nil
}
