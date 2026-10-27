// Package app is the terminal app that opens when rodin3d-mcp runs bare in a
// terminal (plan 2.9): the tasks of the account and one task with its
// previews, the account and wallet, the write permissions, sign-in and
// sign-out, the viewer window and the doctor. A status line shows the
// session and the background syncer on every screen.
//
// Paid operations are not offered here: they run through the agent's tools
// or the one-shot commands, which quote each price first.
package app

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	wapp "github.com/sairaph/mcp-wizard/app"
	"github.com/sairaph/mcp-wizard/app/confirm"
	"github.com/sairaph/mcp-wizard/app/detail"
	"github.com/sairaph/mcp-wizard/app/menu"
	"github.com/sairaph/mcp-wizard/app/table"
	"github.com/sairaph/mcp-wizard/render"
	"github.com/sairaph/mcp-wizard/tui"

	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/install"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// Options configure the app.
type Options struct {
	Version string
	Service *service.Service
	// Open shows a file with the operating system's viewer; nil never opens.
	Open func(path string) error
	// Doctor writes the doctor's report.
	Doctor func(ctx context.Context, w io.Writer)
}

// Run runs the app until the user quits.
func Run(ctx context.Context, opts Options) int {
	m := newModel(ctx, opts)
	return wapp.Run(ctx, m, wapp.Options{Title: domain.BinaryName, Version: opts.Version})
}

const (
	scrMenu wapp.Step = iota + wapp.StepCustom
	scrText           // a read-only text (account, doctor, viewer, sign-out result)
	scrTasks
	scrTask
	scrPickMode
	scrPickView
	scrSignIn
	scrPermissions
	scrSignOut
)

type model struct {
	wapp.AppModel
	ctx  context.Context
	opts Options
	svc  *service.Service

	menu    *menu.Model
	text    *detail.Model
	table   *table.Model
	task    *detail.Model
	picker  *menu.Model
	signIn  *install.SignIn
	perms   *install.Permissions
	signOut *confirm.Model

	rows     []service.TaskRow
	taskID   string // the task on the task screen
	taskNote string // what the last preview or viewer call did
	textSeq  int    // counts the text screens opened
	mode     string // the preview mode picked
	user     string // signed-in account ("" when signed out)
	session  string // auth state
	sync     service.SyncStatus
	syncErr  string
}

func newModel(ctx context.Context, opts Options) *model {
	m := &model{ctx: ctx, opts: opts, svc: opts.Service}
	m.Step = scrMenu
	m.menu = menu.New(domain.BinaryName+" "+opts.Version, m.menuItems)
	m.readStatus()
	return m
}

func (m *model) menuItems() []menu.Item {
	items := []menu.Item{
		{Label: "Tasks", Action: "tasks"},
		{Label: "Account and wallet", Action: "account"},
		{Label: "Permissions", Action: "permissions"},
	}
	if m.user != "" {
		items = append(items, menu.Item{Label: "Sign out (" + m.user + ")", Action: "signout"})
	} else {
		items = append(items, menu.Item{Label: "Sign in", Action: "signin"})
	}
	return append(items,
		menu.Item{Label: "Open the viewer", Action: "viewer"},
		menu.Item{Label: "Run doctor", Action: "doctor"},
		menu.Item{Label: "Quit", Action: "quit"},
	)
}

// --- messages ---

type statusTickMsg struct{}

type textMsg struct {
	seq   int // the text screen the text belongs to
	title string
	body  string
}

type rowsMsg struct {
	rows   []service.TaskRow
	synced bool
	err    error
}

type taskMsg struct {
	id   string
	body string
}

type noteMsg struct {
	id   string
	note string
}

func statusTick() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return statusTickMsg{} })
}

func (m *model) Init() tea.Cmd { return tea.Batch(m.menu.Init(), statusTick()) }

// readStatus reads the session and the syncer's state for the status line.
func (m *model) readStatus() {
	m.user, m.session = "", auth.StateSignedOut
	if c, err := m.svc.Credentials(m.ctx); err == nil && c != nil {
		m.session = c.State()
		if m.session == auth.StateSignedIn {
			m.user = c.Username
		}
	}
	m.sync, m.syncErr = service.SyncStatus{}, ""
	if s, err := m.svc.Sync(); err == nil {
		m.sync = s
	} else {
		m.syncErr = err.Error()
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := m.HandleGlobalKeys(msg); handled {
		if m.signIn != nil {
			m.signIn.Cancel()
		}
		return m, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg)
		return m, nil
	case statusTickMsg:
		m.readStatus()
		return m, statusTick()
	case textMsg:
		if m.Step == scrText && m.textSeq == msg.seq {
			m.text.Title = msg.title
			m.text.SetContent(msg.body)
		}
		m.readStatus() // a sign-out or the doctor may have changed it
		return m, nil
	case rowsMsg:
		if m.Step == scrTasks {
			m.setRows(msg)
		}
		return m, nil
	case taskMsg:
		if m.Step == scrTask && m.taskID == msg.id {
			m.task.SetContent(msg.body)
		}
		return m, nil
	case noteMsg:
		if m.taskID == msg.id {
			m.taskNote = msg.note
		}
		return m, nil
	case wapp.ActionMsg:
		return m.action(msg)
	}
	return m, m.forward(msg)
}

// size is the last terminal size, given to every screen made later.
func (m *model) resize(msg tea.WindowSizeMsg) {
	for _, d := range []*detail.Model{m.text, m.task} {
		if d != nil {
			d.Update(m.shrunk(msg))
		}
	}
	if m.table != nil {
		m.table.Update(m.shrunk(msg))
	}
	if m.signIn != nil {
		m.signIn.SetSize(msg.Width, msg.Height)
	}
}

// shrunk leaves room for the status line under a screen.
func (m *model) shrunk(msg tea.WindowSizeMsg) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: msg.Width, Height: max(msg.Height-3, 5)}
}

func (m *model) size() tea.WindowSizeMsg {
	return m.shrunk(tea.WindowSizeMsg{Width: m.Width, Height: m.Height})
}

// forward passes a message to the screen on show.
func (m *model) forward(msg tea.Msg) tea.Cmd {
	switch m.Step {
	case scrMenu:
		return m.menu.Update(msg)
	case scrText:
		return m.text.Update(msg)
	case scrTasks:
		if k, ok := msg.(tea.KeyMsg); ok && k.String() == "r" {
			return m.loadRows()
		}
		return m.table.Update(msg)
	case scrTask:
		return m.taskKey(msg)
	case scrPickMode, scrPickView:
		if k, ok := msg.(tea.KeyMsg); ok && (k.String() == "esc" || k.String() == "q") {
			m.Step = scrTask
			return nil
		}
		return m.picker.Update(msg)
	case scrSignIn:
		return m.signInUpdate(msg)
	case scrPermissions:
		switch m.perms.Update(msg) {
		case install.PermissionsBack:
			m.Step = scrMenu
		case install.PermissionsQuit:
			m.Quit = true
			return tea.Quit
		}
		return nil
	case scrSignOut:
		return m.signOut.Update(msg)
	}
	return nil
}

func (m *model) action(msg wapp.ActionMsg) (tea.Model, tea.Cmd) {
	switch msg.Source {
	case "menu":
		if msg.Value == "quit" {
			m.Quit = true
			return m, tea.Quit
		}
		action, _ := msg.Data.(string)
		switch m.Step {
		case scrPickMode:
			return m, m.pickedMode(action)
		case scrPickView:
			return m, m.pickedView(action)
		}
		return m, m.open(action)
	case "detail":
		if msg.Value == "back" {
			if m.Step == scrTask {
				m.Step = scrTasks
			} else {
				m.Step = scrMenu
			}
		}
	case "table":
		switch msg.Value {
		case "back":
			m.Step = scrMenu
		case "select":
			i, _ := msg.Data.(int)
			if i >= 0 && i < len(m.table.Rows) {
				row := m.table.Rows[i]
				return m, m.openTask(row[len(row)-1])
			}
		}
	case "confirm":
		if m.Step == scrSignOut {
			if msg.Value != "confirmed" {
				m.Step = scrMenu
				return m, nil
			}
			return m, m.showText("Sign out", "Signing out...", func() string {
				res, err := m.svc.Logout(m.ctx)
				return resultText(res, err)
			})
		}
	}
	return m, nil
}

// open shows the screen of a menu item.
func (m *model) open(action string) tea.Cmd {
	switch action {
	case "quit":
		m.Quit = true
		return tea.Quit
	case "tasks":
		m.Step = scrTasks
		m.table = table.New("Tasks", m.columns(), nil)
		m.table.Update(m.size())
		return m.loadRows()
	case "account":
		return m.showText("Account and wallet", "Reading the account...", func() string {
			res, err := m.svc.Account(m.ctx)
			return resultText(res, err)
		})
	case "permissions":
		m.perms = install.NewPermissions(m.svc.Paths().Config)
		m.Step = scrPermissions
	case "signin":
		m.signIn = install.NewSignIn(m.ctx, m.svc, "Back")
		m.signIn.SetSize(m.Width, m.Height-3)
		m.Step = scrSignIn
		return m.signIn.Init()
	case "signout":
		m.signOut = confirm.New("Sign out of Hyper3D?",
			"This revokes the grant of "+m.user+" on Hyper3D and deletes the tokens on this computer. The task index and downloads stay.",
			"Sign out", "Cancel")
		m.Step = scrSignOut
	case "viewer":
		return m.showText("Viewer", "Opening the viewer window...", func() string {
			res, err := m.svc.OpenViewer(m.ctx, service.ViewerOpenInput{})
			return resultText(res, err)
		})
	case "doctor":
		return m.showText("Doctor", "Running the checks...", func() string {
			var b strings.Builder
			if m.opts.Doctor != nil {
				m.opts.Doctor(m.ctx, &b)
			}
			return b.String()
		})
	}
	return nil
}

// showText opens the text screen and fills it from fn in the background.
func (m *model) showText(title, loading string, fn func() string) tea.Cmd {
	m.text = detail.New(title, "  "+loading)
	m.text.Update(m.size())
	m.Step = scrText
	m.textSeq++
	seq := m.textSeq
	return func() tea.Msg { return textMsg{seq: seq, title: title, body: fn()} }
}

// resultText is a result as the one-shot commands print it, or the error
// with its hint.
func resultText(res service.Result, err error) string {
	if err != nil {
		e := service.Classify(err)
		s := "Error (" + e.Code + "): " + e.Message
		if e.Hint != "" {
			s += "\n" + e.Hint
		}
		return indent(s)
	}
	text, rerr := render.Document{Front: res.Front, Body: res.Body}.String()
	if rerr != nil {
		return indent(res.Body)
	}
	return indent(text)
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

// --- tasks ---

func (m *model) columns() []table.Column {
	fixed := 10 + 18 + 11 + 16 + 17 // id, stage, state, tier, created
	prompt := max(m.Width-fixed-4, 20)
	return []table.Column{
		{Name: "Prompt", Width: prompt}, {Name: "ID", Width: 10}, {Name: "Stage", Width: 18},
		{Name: "State", Width: 11}, {Name: "Tier", Width: 16}, {Name: "Created", Width: 17},
		{Name: "", Width: 0}, // the full id, not shown
	}
}

func (m *model) loadRows() tea.Cmd {
	m.table.Title = "Tasks (loading...)"
	return func() tea.Msg {
		rows, synced, err := m.svc.TaskRows(m.ctx)
		return rowsMsg{rows: rows, synced: synced, err: err}
	}
}

func (m *model) setRows(msg rowsMsg) {
	switch {
	case msg.err != nil:
		e := service.Classify(msg.err)
		m.table.Title = "Tasks: " + e.Message
		if e.Code == render.CodeAuth {
			m.table.Title = "Tasks: not signed in. Sign in from the menu first."
		}
		m.table.SetRows(nil)
		return
	case !msg.synced:
		m.table.Title = "Tasks: the first history sync has not finished; press r to read again."
		m.table.SetRows(nil)
		return
	}
	m.rows = msg.rows
	out := make([]table.Row, 0, len(msg.rows))
	for _, r := range msg.rows {
		created := r.Created
		if t, err := time.Parse(time.RFC3339, r.Created); err == nil {
			created = t.Local().Format("2006-01-02 15:04")
		}
		out = append(out, table.Row{r.Prompt, short(r.ID), r.Stage, r.State, r.Tier, created, r.ID})
	}
	m.table.Title = fmt.Sprintf("Tasks (%d, newest first; s sorts, r reads again)", len(out))
	m.table.SetRows(out)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (m *model) openTask(id string) tea.Cmd {
	m.taskID, m.taskNote = id, ""
	m.task = detail.New("Task "+short(id), "  Reading the task...")
	m.task.Update(m.size())
	m.Step = scrTask
	return func() tea.Msg {
		res, err := m.svc.GetTask(m.ctx, id)
		return taskMsg{id: id, body: resultText(res, err)}
	}
}

func (m *model) taskKey(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "p":
			items := make([]menu.Item, 0, len(service.PreviewModes()))
			for _, mode := range service.PreviewModes() {
				items = append(items, menu.Item{Label: mode, Action: mode})
			}
			m.picker = menu.New("Preview of "+short(m.taskID)+": which picture? (esc back)", func() []menu.Item { return items })
			m.Step = scrPickMode
			return nil
		case "v":
			id := m.taskID
			m.taskNote = "Opening the viewer window..."
			return func() tea.Msg {
				res, err := m.svc.OpenViewer(m.ctx, service.ViewerOpenInput{Content: &viewer.Content{Task: &viewer.TaskRef{TaskID: id}}})
				if err != nil {
					e := service.Classify(err)
					return noteMsg{id: id, note: e.Message}
				}
				_ = res
				return noteMsg{id: id, note: "The viewer window shows the task."}
			}
		}
	}
	return m.task.Update(msg)
}

func (m *model) pickedMode(mode string) tea.Cmd {
	m.mode = mode
	if !slices.Contains(service.RenderModes(), mode) {
		return m.preview(mode, "") // a server image has no view
	}
	items := make([]menu.Item, 0, len(service.PreviewViews()))
	for _, v := range service.PreviewViews() {
		items = append(items, menu.Item{Label: v, Action: v})
	}
	m.picker = menu.New("Preview "+mode+": from which side? (esc back)", func() []menu.Item { return items })
	m.Step = scrPickView
	return nil
}

func (m *model) pickedView(view string) tea.Cmd { return m.preview(m.mode, view) }

// preview saves the picture and opens it with the system's image viewer.
func (m *model) preview(mode, view string) tea.Cmd {
	id := m.taskID
	m.Step = scrTask
	m.taskNote = "Making the " + mode + " preview..."
	return func() tea.Msg {
		res, err := m.svc.Preview(m.ctx, service.PreviewInput{TaskID: id, Mode: mode, View: view})
		if err != nil {
			e := service.Classify(err)
			return noteMsg{id: id, note: e.Message}
		}
		file := service.PreviewFile(res)
		if m.opts.Open != nil && file != "" {
			if err := m.opts.Open(file); err == nil {
				return noteMsg{id: id, note: "Opened " + file}
			}
		}
		return noteMsg{id: id, note: "Saved " + file}
	}
}

// --- sign-in ---

func (m *model) signInUpdate(msg tea.Msg) tea.Cmd {
	ev, cmd := m.signIn.Update(msg)
	switch ev {
	case install.SignInDone, install.SignInSkipped, install.SignInBack:
		m.signIn.Cancel()
		m.readStatus()
		m.Step = scrMenu
	case install.SignInQuit:
		m.Quit = true
		return tea.Quit
	}
	return cmd
}

// --- view ---

func (m *model) View() string {
	var body string
	switch m.Step {
	case scrText:
		body = m.text.View()
	case scrTasks:
		body = m.table.View()
	case scrTask:
		body = m.task.View()
		hint := "  p preview (opens the picture) · v show in the viewer window"
		if m.taskNote != "" {
			hint = "  " + m.taskNote + "\n" + hint
		}
		body += "\n" + tui.DefaultTheme.Styles().Hint.Render(hint)
	case scrPickMode, scrPickView:
		body = m.picker.View()
	case scrSignIn:
		body = section("Sign in to Hyper3D", m.signIn.View())
	case scrPermissions:
		body = section("Permissions", m.perms.View("back"))
	case scrSignOut:
		body = m.signOut.View()
	default:
		body = m.menu.View()
	}
	return body + "\n\n" + m.statusLine()
}

func section(title, content string) string {
	return tui.DefaultTheme.Styles().Title.Render("  "+title) + "\n\n" + content
}

// statusLine is the session and the syncer, on every screen.
func (m *model) statusLine() string {
	st := tui.DefaultTheme.Styles()
	var parts []string
	switch {
	case m.user != "":
		parts = append(parts, "signed in as "+m.user)
	case m.session == auth.StateNeedsLogin:
		parts = append(parts, "session ended: sign in again")
	default:
		parts = append(parts, "not signed in")
	}
	switch {
	case m.syncErr != "":
		parts = append(parts, "task index: "+m.syncErr)
	case m.sync.Running:
		s := "sync " + orDash(m.sync.Phase)
		if !m.sync.LastSync.IsZero() {
			s += ", last " + m.sync.LastSync.Local().Format("15:04")
		}
		parts = append(parts, s, fmt.Sprintf("%d tasks", m.sync.Tasks))
	default:
		parts = append(parts, "sync stopped", fmt.Sprintf("%d tasks", m.sync.Tasks))
	}
	line := st.Dim.Render("  " + strings.Join(parts, " · "))
	if m.sync.Error != "" {
		line += "\n" + st.Err.Render("  last sync error: "+m.sync.Error)
	}
	return line
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
