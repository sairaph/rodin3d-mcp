package install

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sairaph/mcp-wizard/tui"

	"github.com/sairaph/rodin3d-mcp/internal/config"
)

// Permissions is the screen that sets the [permission] table of config.toml,
// shared by the installer and the app: one row per key, every key denied
// until the user allows it (D5).
type Permissions struct {
	path    string
	perms   config.Permissions
	loadErr error
	exists  bool
	cursor  int
	message string
	dirty   bool
}

// PermissionsEvent is what the screen asks its host to do after a key.
type PermissionsEvent int

const (
	PermissionsNone  PermissionsEvent = iota
	PermissionsSaved                  // the file was written
	PermissionsBack                   // esc: leave without writing
	PermissionsQuit                   // ctrl+c
)

// NewPermissions reads path. A missing file shows every key denied; a
// malformed one shows every key denied and the problem, and saving replaces
// it.
func NewPermissions(path string) *Permissions {
	p := &Permissions{path: path}
	p.Reload()
	return p
}

// Reload reads the file again and drops unsaved changes.
func (p *Permissions) Reload() {
	cfg, err := config.Load(p.path)
	p.perms, p.loadErr, p.dirty, p.message = cfg.Permission, err, false, ""
	_, statErr := os.Stat(p.path)
	p.exists = statErr == nil
}

// Values returns the values on screen.
func (p *Permissions) Values() config.Permissions { return p.perms }

// Save writes the values on screen.
func (p *Permissions) Save() error {
	if err := config.Save(p.path, p.perms); err != nil {
		return err
	}
	p.loadErr, p.exists, p.dirty = nil, true, false
	return nil
}

// Update handles a key.
func (p *Permissions) Update(msg tea.Msg) PermissionsEvent {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return PermissionsNone
	}
	n := len(config.Keys)
	switch k.String() {
	case "ctrl+c":
		return PermissionsQuit
	case "up", "k":
		p.cursor = (p.cursor + n - 1) % n
	case "down", "j":
		p.cursor = (p.cursor + 1) % n
	case " ", "x":
		key := config.Keys[p.cursor].Name
		next := config.Allow
		if p.perms.Allowed(key) {
			next = config.Deny
		}
		_ = p.perms.Set(key, next)
		p.dirty, p.message = true, ""
	case "a":
		// Allow every key, or deny every key when all are allowed.
		all := true
		for _, key := range config.Keys {
			all = all && p.perms.Allowed(key.Name)
		}
		value := config.Allow
		if all {
			value = config.Deny
		}
		for _, key := range config.Keys {
			_ = p.perms.Set(key.Name, value)
		}
		p.dirty, p.message = true, ""
	case "enter":
		if err := p.Save(); err != nil {
			p.message = "Could not save: " + err.Error()
			return PermissionsNone
		}
		p.message = "Saved."
		return PermissionsSaved
	case "esc":
		return PermissionsBack
	}
	return PermissionsNone
}

// Hints are the screen's keys.
func (p *Permissions) Hints(back string) []tui.Hint {
	return []tui.Hint{
		{Key: "↑↓", Label: "move"}, {Key: "space", Label: "allow/deny"}, {Key: "a", Label: "all"},
		{Key: "enter", Label: "save"}, {Key: "esc", Label: back},
	}
}

// View renders the screen without chrome.
func (p *Permissions) View(back string) string {
	theme := tui.DefaultTheme
	st := theme.Styles()
	var b strings.Builder
	b.WriteString("  Which operations may an AI agent run without asking you? Each one spends\n")
	b.WriteString("  credits or changes a task on Hyper3D. Every one is denied until you allow\n")
	b.WriteString("  it; reading tasks, previews and downloads needs no permission.\n\n")
	width := 0
	for _, k := range config.Keys {
		width = max(width, len(k.Name))
	}
	for i, k := range config.Keys {
		pointer := " "
		if i == p.cursor {
			pointer = st.Cursor.Render(">")
		}
		value := st.Off.Render("deny ")
		if p.perms.Allowed(k.Name) {
			value = st.On.Render("allow")
		}
		fmt.Fprintf(&b, " %s %s  %-*s  %s\n", pointer, value, width, k.Name, st.Dim.Render(k.Tools))
	}
	b.WriteString("\n")
	switch {
	case p.loadErr != nil && errors.Is(p.loadErr, config.ErrMalformed):
		b.WriteString(st.Err.Render("  "+p.loadErr.Error()) + "\n")
		b.WriteString(st.Dim.Render("  Every operation is denied until it is fixed; saving here replaces the file.") + "\n")
	case p.loadErr != nil:
		b.WriteString(st.Err.Render("  "+p.loadErr.Error()) + "\n")
	case !p.exists:
		b.WriteString(st.Dim.Render("  "+tui.ShortPath(p.path)+" does not exist yet: saving creates it.") + "\n")
	default:
		b.WriteString(st.Dim.Render("  Saved in "+tui.ShortPath(p.path)+"; every call reads it again.") + "\n")
	}
	if p.dirty {
		b.WriteString(st.Hint.Render("  Not saved yet: enter saves, esc drops the changes.") + "\n")
	}
	if p.message != "" {
		style := st.Err
		if p.message == "Saved." {
			style = st.On
		}
		b.WriteString("\n" + style.Render("  "+p.message) + "\n")
	}
	b.WriteString("\n" + tui.Footer(theme, tui.Hints(theme, p.Hints(back)...)) + "\n")
	return b.String()
}
