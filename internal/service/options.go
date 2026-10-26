package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/catalog"
	"github.com/sairaph/rodin3d-mcp/internal/config"
	"github.com/sairaph/rodin3d-mcp/internal/daemon"
	"github.com/sairaph/rodin3d-mcp/internal/price"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
)

type privilegeFront struct {
	Name    string `yaml:"name"`
	Unlocks string `yaml:"unlocks"`
	Has     *bool  `yaml:"has,omitempty"`
}

type recipeExample struct {
	Prompt string `yaml:"prompt"`
	ID     string `yaml:"id"`
}

type recipeFront struct {
	Count           int             `yaml:"count"`
	Model           string          `yaml:"model"`
	Tier            string          `yaml:"tier,omitempty"`
	Mesh            string          `yaml:"mesh,omitempty"`
	QualityOverride string          `yaml:"quality_override,omitempty"`
	TextureMode     string          `yaml:"texture_mode,omitempty"`
	Escore          string          `yaml:"escore,omitempty"`
	Format          string          `yaml:"format,omitempty"`
	MultiView       int             `yaml:"multi_view_tasks,omitempty"`
	Examples        []recipeExample `yaml:"examples"`
}

type optionsFront struct {
	Plan           string             `yaml:"plan,omitempty"`
	Models         []catalog.Model    `yaml:"models"`
	Settings       []catalog.Setting  `yaml:"settings"`
	ViewTags       []catalog.ViewTag  `yaml:"view_tags"`
	DefaultWeights []catalog.Weights  `yaml:"default_weights"`
	PreviewModes   []previewFront     `yaml:"preview_modes"`
	Privileges     []privilegeFront   `yaml:"privileges"`
	Recipes        []recipeFront      `yaml:"recipes"`
	Operations     []operationFront   `yaml:"operations"`
	Permissions    config.Permissions `yaml:"permissions"`
}

type operationFront struct {
	Name       string `yaml:"name"`
	Permission string `yaml:"permission"`
}

// Options is get_generation_options: the settings catalogue, the gates of
// this account's plan, and the recipes found in its history.
func (s *Service) Options(ctx context.Context, model string) (Result, error) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model != "" && model != "gen-2.5" && model != "gen-1.5" {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("model %q is not gen-2.5 or gen-1.5.", model), "Call get_generation_options without model, or with gen-2.5 or gen-1.5.")
	}
	front := optionsFront{ViewTags: catalog.ViewTags, DefaultWeights: catalog.DefaultWeights}
	for _, m := range catalog.Models {
		if model == "" || m.Name == model {
			front.Models = append(front.Models, m)
		}
	}
	for _, st := range catalog.Settings {
		if model != "" && st.Models != "" && !strings.HasPrefix(st.Models, model) {
			continue
		}
		front.Settings = append(front.Settings, st)
	}
	for _, stg := range assets.Stages {
		front.PreviewModes = append(front.PreviewModes, previewFront{Stage: stg, Modes: assets.Modes[stg]})
	}
	for _, o := range price.Operations {
		front.Operations = append(front.Operations, operationFront{Name: o.Name, Permission: o.Permission})
	}
	cfg, _ := s.Config()
	front.Permissions = cfg.Permission

	var info *rodin.UserInfo
	if _, err := s.signedIn(ctx); err == nil {
		if got, err := s.client.GetInfo(ctx); err == nil {
			info = got
			front.Plan = got.Meta.Subscriptions.HighestLevel
		}
	}
	for _, p := range catalog.Privileges {
		pf := privilegeFront{Name: p.Name, Unlocks: p.Unlocks}
		if info != nil {
			has := info.HasPrivilege(p.Name)
			pf.Has = &has
		}
		front.Privileges = append(front.Privileges, pf)
	}
	front.Recipes = s.recipes(model)
	if front.Recipes == nil {
		front.Recipes = []recipeFront{}
	}

	var b strings.Builder
	b.WriteString("Settings per stage with their web field, range and default; view tags and default weights for multi-view inputs; the server image modes get_preview offers per stage.")
	if info != nil {
		fmt.Fprintf(&b, " Privileges show what the %s plan of this account allows.", orUnknown(front.Plan))
	} else {
		b.WriteString(" Sign in (login) to see which privileges this account has.")
	}
	if len(front.Recipes) > 0 {
		fmt.Fprintf(&b, "\n\nRecipes: %d setting combinations found in the account's %s, most used first.", len(front.Recipes), "history")
	} else {
		b.WriteString("\n\nNo recipes yet: the task history has not been synced (list_tasks starts it).")
	}
	b.WriteString("\n\nNext: get_task on a recipe example for its full settings; quote_cost for the price of an operation.")
	return Result{Front: front, Body: b.String()}, nil
}

// recipes groups the stored cards by their key settings.
func (s *Service) recipes(model string) []recipeFront {
	st, err := s.Store()
	if err != nil {
		return nil
	}
	cards, err := st.Cards()
	if err != nil {
		return nil
	}
	tasks, _ := st.Tasks()
	created := map[string]string{}
	for _, t := range tasks {
		created[t.UUID] = t.Created
	}
	type group struct {
		recipeFront
		newest string
	}
	groups := map[string]*group{}
	for uuid, raw := range cards {
		card, err := rodin.ParseCard(raw)
		if err != nil {
			continue
		}
		sum := daemon.Summarize(card)
		if sum.Mode == "" || (model != "" && sum.Mode != model) {
			continue
		}
		r := recipeFront{
			Model: sum.Mode, Tier: sum.Tier, Mesh: card.Setting("mesh_export_type"),
			QualityOverride: card.Setting("quality_override"), TextureMode: card.Setting("texture_mode"),
			Escore: card.Setting("escore"), Format: card.Setting("geometry_file_format"),
		}
		key := strings.Join([]string{r.Model, r.Tier, r.Mesh, r.QualityOverride, r.TextureMode, r.Escore, r.Format}, "|")
		g := groups[key]
		if g == nil {
			g = &group{recipeFront: r}
			groups[key] = g
		}
		g.Count++
		if len(sum.Views) > 1 {
			g.MultiView++
		}
		g.Examples = append(g.Examples, recipeExample{Prompt: clip(card.Prompt, 70), ID: uuid})
		if c := created[uuid]; c > g.newest {
			g.newest = c
		}
	}
	out := make([]recipeFront, 0, len(groups))
	list := make([]*group, 0, len(groups))
	for _, g := range groups {
		list = append(list, g)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Count != list[j].Count {
			return list[i].Count > list[j].Count
		}
		return list[i].newest > list[j].newest
	})
	for _, g := range list {
		sort.Slice(g.Examples, func(i, j int) bool { return created[g.Examples[i].ID] > created[g.Examples[j].ID] })
		if len(g.Examples) > 3 {
			g.Examples = g.Examples[:3]
		}
		out = append(out, g.recipeFront)
	}
	return out
}

// --- quote_cost ---

// QuoteInput is the input of quote_cost.
type QuoteInput struct {
	Operation    string
	TaskID       string
	Model        string
	Tier         string
	TextureMode  string
	SmartLowPoly bool
	Resolution   string
	Geometry     string
	Budget       string
	Batch        int
}

type permissionFront struct {
	Key   string `yaml:"key"`
	Value string `yaml:"value"`
	File  string `yaml:"file"`
}

type quoteFront struct {
	Operation  string          `yaml:"operation"`
	Quote      price.Quote     `yaml:"quote"`
	Wallet     Wallet          `yaml:"wallet"`
	Permission permissionFront `yaml:"permission"`
	Affordable *bool           `yaml:"affordable,omitempty"`
	Task       *TaskFront      `yaml:"task,omitempty"`
}

// Quote is quote_cost: the price of an operation from the label table, the
// wallet, and whether config.toml allows it.
func (s *Service) Quote(ctx context.Context, in QuoteInput) (Result, error) {
	key, ok := price.PermissionKey(in.Operation)
	if !ok {
		_, err := price.For(price.Input{Operation: in.Operation}, price.Account{})
		return Result{}, err
	}
	if _, err := s.signedIn(ctx); err != nil {
		return Result{}, err
	}
	pin := price.Input{
		Operation: in.Operation, Model: in.Model, Tier: in.Tier, TextureMode: in.TextureMode, SmartLowPoly: in.SmartLowPoly,
		Resolution: in.Resolution, Geometry: in.Geometry, Budget: in.Budget, Batch: in.Batch,
	}
	var task *TaskFront
	if strings.TrimSpace(in.TaskID) != "" {
		uuid, err := s.resolveTask(in.TaskID)
		if err != nil {
			return Result{}, err
		}
		card, err := s.client.Card(ctx, uuid)
		if err != nil {
			return Result{}, err
		}
		t := s.taskFront(card)
		task = &t
		if pin.Model == "" {
			pin.Model = stage.Model(card.Setting("generation_mode"))
		}
		if pin.Tier == "" {
			pin.Tier = stage.Tier(card.Setting("brilliant_mode"))
		}
		if ts, err := time.Parse(time.RFC3339, card.Time); err == nil {
			pin.TaskCreated = ts
		}
	}
	info, err := s.client.GetInfo(ctx)
	if err != nil {
		return Result{}, err
	}
	q, err := price.For(pin, price.Account{Privileges: info.Meta.Privileges, Plan: info.Meta.Subscriptions.HighestLevel})
	if err != nil {
		return Result{}, err
	}
	cfg, cfgErr := s.Config()
	value, _ := cfg.Permission.Get(key)
	front := quoteFront{
		Operation: in.Operation, Quote: q, Wallet: WalletOf(info), Task: task,
		Permission: permissionFront{Key: key, Value: value, File: s.paths.Config},
	}
	if q.Known && q.Credits != nil && *q.Credits > 0 { // a free operation needs no balance
		// Frozen credits are counted as spent: whether prepaid_available already
		// excludes them is not measured, and erring low never overspends.
		ok := front.Wallet.Available-front.Wallet.Frozen >= *q.Credits
		front.Affordable = &ok
	}

	var b strings.Builder
	switch {
	case q.Known && *q.Credits == 0:
		fmt.Fprintf(&b, "%s costs nothing: %s.", in.Operation, q.FreeReason)
	case q.Known:
		fmt.Fprintf(&b, "%s costs %s credits (%s).", in.Operation, num(*q.Credits), q.ChargedAt)
	default:
		fmt.Fprintf(&b, "The price of %s is not known: %s.", in.Operation, q.Basis)
	}
	if q.FrozenAtSubmit != nil {
		fmt.Fprintf(&b, " The server was seen to freeze %s credits at submit; the freeze is released when the draft is never confirmed.", num(*q.FrozenAtSubmit))
	}
	fmt.Fprintf(&b, "\n\nWallet: %s credits available, %s frozen.", num(front.Wallet.Available), num(front.Wallet.Frozen))
	fmt.Fprintf(&b, " Permission %s is %q in %s.", key, value, s.paths.Config)
	if cfgErr != nil {
		fmt.Fprintf(&b, " (The file has a problem, so every write is denied: %s.)", cfgErr)
	}
	b.WriteString("\n\nNext: get_account for the wallet and every permission.")
	return Result{Front: front, Body: b.String()}, nil
}
