package install

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sairaph/mcp-wizard/flow"
	"github.com/sairaph/mcp-wizard/harness"

	"github.com/sairaph/rodin3d-mcp/internal/config"
)

// toggleStep stands in for mcp-wizard's client list: space flips the entry
// of client a, as installer.ToggleHarness does.
type toggleStep struct{ flow.Step[State] }

func (toggleStep) Update(msg tea.Msg, state *State) (flow.Directive, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == " " {
		state.Harness.Selected["a"] = !state.Harness.Selected["a"]
	}
	return flow.Continue, nil
}

// The client list's view shows every entry of Selected as selected, so
// harnessStep keeps only the true ones: space turns a client off on screen
// too, and on again.
func TestHarnessStepDropsUnselected(t *testing.T) {
	step := harnessStep{toggleStep{}}
	state := &State{}
	state.Harness.Selected = map[harness.ID]bool{"a": true, "b": true}
	space := tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	step.Update(space, state)
	if _, ok := state.Harness.Selected["a"]; ok || !state.Harness.Selected["b"] {
		t.Fatalf("after space: %v, want only b", state.Harness.Selected)
	}
	step.Update(space, state)
	if !state.Harness.Selected["a"] || !state.Harness.Selected["b"] || len(state.Harness.Selected) != 2 {
		t.Fatalf("after space again: %v, want a and b", state.Harness.Selected)
	}
}

// The permissions screen starts with every key allowed; space denies the
// key under the cursor and enter saves it.
func TestPermissionsStartAllowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	p := NewPermissions(path)
	for _, k := range config.Keys {
		if !p.Values().Allowed(k.Name) {
			t.Fatalf("%s denied on a new screen", k.Name)
		}
	}
	if v := p.View("back"); strings.Contains(v, "deny  ") || !strings.Contains(v, "Every one is allowed until you deny") {
		t.Fatalf("view:\n%s", v)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if ev := p.Update(tea.KeyMsg{Type: tea.KeyEnter}); ev != PermissionsSaved {
		t.Fatalf("enter: event %d, message %q", ev, p.message)
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Permission.Allowed(config.Keys[0].Name) || len(cfg.Permission.Denied()) != 1 {
		t.Fatalf("saved %+v (%v), want only %s denied", cfg.Permission, err, config.Keys[0].Name)
	}
}
