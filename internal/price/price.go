// Package price quotes what an operation costs. The web app has no price
// function: every cost it shows is a fixed button label, and the server
// decides and freezes the real amount (notes 14, section 4). The quotes here
// are those labels plus the wallet deltas measured so far (notes 10, 13);
// where neither exists the quote says it is not known.
package price

import (
	"fmt"
	"strings"
	"time"
)

// Operations quote_cost accepts, each with the [permission] key that allows it.
var Operations = []struct{ Name, Permission string }{
	{"generate_model", "generate"},
	{"confirm_geometry", "confirm_geometry"},
	{"generate_texture", "generate_texture"},
	{"confirm_material", "confirm_material"},
	{"export_model", "export"},
	{"fork_task", "fork"},
	{"generate_agentic", "agentic"},
	{"split_parts", "split_parts"},
	{"rig_model", "rig"},
	{"animate_model", "animate"},
	{"texture_model", "texture_model"},
}

// PermissionKey returns the [permission] key of operation.
func PermissionKey(operation string) (string, bool) {
	for _, o := range Operations {
		if o.Name == operation {
			return o.Permission, true
		}
	}
	return "", false
}

// Input is what a quote depends on. Empty fields are unknown; Task fills the
// ones a task's card answers.
type Input struct {
	Operation    string
	Model        string // gen-2.5 (default) | gen-1.5
	Tier         string // extreme-low low medium high extreme-high (Gen-2.5)
	TextureMode  string // extreme-low .. extreme-high
	SmartLowPoly bool
	Resolution   string // 1k 2k 4k 6k 12k (pack resolutions; 8K is the sharp_texture option)
	Geometry     string // base | lod | high_poly
	Budget       string // basic standard pro (Agentic)
	Batch        int
	// TaskCreated is the creation time of the task an operation acts on, for
	// the 30-day fork rule; zero when not known.
	TaskCreated time.Time
}

// Quote is the price of one call.
type Quote struct {
	Credits    *float64 `yaml:"credits"`               // nil: not known
	Known      bool     `yaml:"known"`                 // Credits is set
	ChargedAt  string   `yaml:"charged_at,omitempty"`  // when the amount is taken
	FreeReason string   `yaml:"free_reason,omitempty"` // why it costs nothing
	Basis      string   `yaml:"basis"`                 // where the number comes from
	// FrozenAtSubmit is an amount the server was seen to freeze at submit
	// although the label says free; nil when none was measured.
	FrozenAtSubmit *float64 `yaml:"frozen_at_submit,omitempty"`
}

// Gate is a plan gate that refuses the operation for this account.
type Gate struct {
	Reason string
}

func (g *Gate) Error() string { return g.Reason }

// InvalidInput is an input the quote cannot be made for.
type InvalidInput struct{ Reason string }

func (e *InvalidInput) Error() string { return e.Reason }

// Account is what the gates read from user/get_info.
type Account struct {
	Privileges []string
	Plan       string // subscriptions.highest_level, e.g. Creator
}

func (a Account) has(p string) bool {
	for _, x := range a.Privileges {
		if x == p {
			return true
		}
	}
	return false
}

func credits(v float64) *float64 { return &v }

var tiers = []string{"extreme-low", "low", "medium", "high", "extreme-high"}

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// For returns the quote of in for account, a *Gate when the account's plan
// refuses the operation, or an *InvalidInput.
func For(in Input, acct Account) (Quote, error) {
	switch in.Operation {
	case "generate_model":
		return generate(in, acct)
	case "confirm_geometry":
		if in.SmartLowPoly {
			if !acct.has("RodinRemeshAdditionalFeatures") {
				return Quote{}, &Gate{"smart low-poly needs the RodinRemeshAdditionalFeatures privilege (Creator plan or higher)"}
			}
			return Quote{Credits: credits(2), Known: true, ChargedAt: "confirm", Basis: `web label "2 Credits" on Confirm in Smart Low-poly mode`}, nil
		}
		return Quote{Credits: credits(0.5), Known: true, ChargedAt: "confirm", Basis: `web label "0.5 Credits" on Confirm (seen on High and Extreme-Low drafts)`}, nil
	case "generate_texture":
		mode := strings.ToLower(in.TextureMode)
		if mode == "" {
			mode = "high" // the web panel's default tier
		}
		if !oneOf(mode, tiers) {
			return Quote{}, &InvalidInput{fmt.Sprintf("texture_mode %q is not one of %s", in.TextureMode, strings.Join(tiers, ", "))}
		}
		switch mode {
		case "extreme-high":
			return Quote{Credits: credits(2), Known: true, ChargedAt: "submit (frozen), taken on completion", Basis: `web label "2 Credits" for the Extreme High texture tier`}, nil
		case "low":
			return Quote{Credits: credits(0.5), Known: true, ChargedAt: "submit (frozen), taken on completion", Basis: "wallet delta of a texture_mode low run on 2026-10-07 (the web shows no label)"}, nil
		}
		return unknown("the web shows no price for texture tier " + mode + " and it has not been measured"), nil
	case "confirm_material":
		return unknown("the web shows no price for Confirm on the material stage and it has not been measured"), nil
	case "export_model":
		return export(in, acct)
	case "fork_task":
		if !in.TaskCreated.IsZero() && time.Since(in.TaskCreated) > 30*24*time.Hour {
			return Quote{}, &Gate{"the web offers fork only for tasks younger than 30 days"}
		}
		return Quote{Credits: credits(0.5), Known: true, ChargedAt: "submit", Basis: `web label "Revert to branch. (0.5 Credits)"`}, nil
	case "generate_agentic":
		switch strings.ToLower(in.Budget) {
		case "basic", "":
			return Quote{Credits: credits(0.5), Known: true, ChargedAt: "submit (frozen), taken during the run", Basis: "web Agentic timer: basic ~3 min, 0.5 credits (same amount measured on an official-MCP run, 2026-10-26)"}, nil
		case "standard":
			return Quote{Credits: credits(1), Known: true, ChargedAt: "submit (frozen), taken during the run", Basis: "web Agentic timer: standard ~5 min, 1 credit"}, nil
		case "pro":
			return Quote{Credits: credits(2), Known: true, ChargedAt: "submit (frozen), taken during the run", Basis: "web Agentic timer: pro ~10 min, 2 credits"}, nil
		}
		return Quote{}, &InvalidInput{fmt.Sprintf("budget %q is not one of basic, standard, pro", in.Budget)}
	case "split_parts":
		return Quote{Credits: credits(0.5), Known: true, ChargedAt: "submit (frozen), taken on completion", Basis: `web label "0.5 Credits" on Bang to Parts (same amount measured on an official-MCP run, 2026-10-26)`}, nil
	case "rig_model":
		return unknown("the web shows no price for rigging and it has not been measured"), nil
	case "animate_model":
		return unknown("the web shows no price for animation packs and it has not been measured"), nil
	case "texture_model":
		return unknown("the web shows no price for texturing an uploaded model and it has not been measured"), nil
	}
	names := make([]string, len(Operations))
	for i, o := range Operations {
		names[i] = o.Name
	}
	return Quote{}, &InvalidInput{fmt.Sprintf("operation %q is not one of %s", in.Operation, strings.Join(names, ", "))}
}

func unknown(why string) Quote {
	return Quote{Known: false, Basis: why}
}

func generate(in Input, acct Account) (Quote, error) {
	if in.Batch > 1 {
		plan := strings.ToLower(acct.Plan)
		if plan != "business" && plan != "enterprise" {
			return Quote{}, &Gate{fmt.Sprintf("batch %d needs the Business plan (this account: %s)", in.Batch, acct.Plan)}
		}
		// The web shows no price for a batch and none was measured: one
		// draft's label times the count is a guess, so no number.
		return unknown(fmt.Sprintf("the price of a batch of %d drafts is not shown by the web and has not been measured", in.Batch)), nil
	}
	model := strings.ToLower(in.Model)
	if model == "" {
		model = "gen-2.5"
	}
	switch model {
	case "gen-1.5":
		return Quote{Credits: credits(0), Known: true, ChargedAt: "confirm_geometry",
			FreeReason: "Free Preview: the Gen-1.5 draft is free; confirming the geometry costs credits",
			Basis:      `web label "Free Preview" on GENERATE for all four Gen-1.5 modes`}, nil
	case "gen-2.5":
	default:
		return Quote{}, &InvalidInput{fmt.Sprintf("model %q is not gen-2.5 or gen-1.5", in.Model)}
	}
	tier := strings.ToLower(in.Tier)
	if tier == "" {
		return Quote{}, &InvalidInput{"a Gen-2.5 quote needs tier: one of " + strings.Join(tiers, ", ")}
	}
	switch tier {
	case "extreme-low", "low", "medium":
		q := Quote{Credits: credits(0), Known: true, ChargedAt: "confirm_geometry",
			FreeReason: "Free Preview: the draft is free; confirming the geometry costs credits",
			Basis:      `web label "Free Preview" on GENERATE for tier ` + tier}
		if tier == "extreme-low" {
			// 2026-10-07: the web froze 0.5 at an Extreme-Low submit; the
			// freeze is released when the draft is never confirmed.
			q.FrozenAtSubmit = credits(0.5)
		}
		return q, nil
	case "high":
		return Quote{Credits: credits(0.5), Known: true, ChargedAt: "submit", Basis: `web label "0.5 Credits" on GENERATE for tier high`}, nil
	case "extreme-high":
		if !acct.has("RodinUseAdditionalImage") {
			return Quote{}, &Gate{"tier extreme-high needs the RodinUseAdditionalImage privilege (Creator plan or higher)"}
		}
		return Quote{Credits: credits(1), Known: true, ChargedAt: "submit", Basis: `web label "1 Credits" on GENERATE for tier extreme-high`}, nil
	}
	return Quote{}, &InvalidInput{fmt.Sprintf("tier %q is not one of %s", in.Tier, strings.Join(tiers, ", "))}
}

func export(in Input, acct Account) (Quote, error) {
	res := strings.ToLower(in.Resolution)
	high := false
	switch res {
	case "", "1k", "2k", "6k":
	case "4k", "12k":
		high = true
	default:
		if res == "8k" {
			return Quote{}, &InvalidInput{"8k is not a pack resolution: the web's 8K chip is the sharp_texture option of generate_texture; packs are 1k, 2k, 6k (Basic) or 4k, 12k (High)"}
		}
		return Quote{}, &InvalidInput{fmt.Sprintf("resolution %q is not one of 1k, 2k, 4k, 6k, 12k", in.Resolution)}
	}
	geo := strings.ToLower(in.Geometry)
	switch geo {
	case "", "base", "lod":
	case "high_poly":
		high = true
	default:
		return Quote{}, &InvalidInput{fmt.Sprintf("geometry %q is not one of base, lod, high_poly", in.Geometry)}
	}
	if high && !acct.has("RodinPackHighResolution") {
		return Quote{}, &Gate{"4K and 12K packs and high-poly geometry need the RodinPackHighResolution privilege (Business plan); use 2k (or 6k for extreme-high textures) with base geometry"}
	}
	switch strings.ToLower(in.Tier) {
	case "extreme-low", "low":
		return Quote{Credits: credits(0), Known: true, FreeReason: "downloads of Gen-2.5 Extreme-Low and Low models are free",
			Basis: "web pack tip (i18n TIP_PACK_TITLE_3)"}, nil
	}
	return unknown("the web shows no price on the pack panel and it has not been measured"), nil
}
