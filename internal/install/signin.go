package install

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/sairaph/mcp-wizard/tui"

	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/service"
)

// SignIn is the Hyper3D sign-in screen of the installer and the app (D20):
// it starts a sign-in and opens the browser of this computer; c copies the
// link for any other device, and a QR code of it shows when the terminal
// fits it. The loopback listener finishes the sign-in when the browser runs
// here; otherwise p opens a field for the final address (the page that
// cannot connect to 127.0.0.1). The link itself is never printed: cut at
// the terminal's width it does not survive a copy from the screen.
type SignIn struct {
	ctx context.Context
	svc *service.Service
	// Skip is the label of the option that leaves without signing in.
	skip string

	phase   signPhase
	cursor  int
	user    string // the account signed in (choice and done phases)
	started *auth.Started
	opened  bool
	pasting bool   // the paste field is shown (p)
	copied  string // the confirmation of c
	osc52   string // the OSC 52 sequence of a copy through the terminal
	input   string
	message string
	frame   int
	width   int
	height  int
}

// copyText puts text on the system clipboard (tests replace it).
var copyText = clipboard.WriteAll

type signPhase int

const (
	signLoading    signPhase = iota // reading the stored session
	signChoice                      // sign in, keep the session, or skip
	signStarting                    // registering the client, binding the listener
	signWaiting                     // link, QR and paste field shown
	signCompleting                  // exchanging a pasted address
	signDone                        // signed in
)

// SignInEvent is what the screen asks its host to do after a message.
type SignInEvent int

const (
	SignInNone    SignInEvent = iota
	SignInDone                // signed in, or kept the session: continue
	SignInSkipped             // the user chose not to sign in now
	SignInBack                // esc on the first screen
	SignInQuit                // ctrl+c
)

// NewSignIn returns the screen. skip labels the option that leaves without
// signing in ("Skip: ..." in the installer, "Back" in the app).
func NewSignIn(ctx context.Context, svc *service.Service, skip string) *SignIn {
	return &SignIn{ctx: ctx, svc: svc, skip: skip}
}

// User is the account signed in once the screen is done.
func (s *SignIn) User() string { return s.user }

type signLoadedMsg struct{ user string }

type signStartedMsg struct {
	started *auth.Started
	opened  bool
	err     error
}

type signOutcomeMsg struct {
	started *auth.Started
	outcome auth.Outcome
}

type signCompletedMsg struct {
	creds *auth.Credentials
	err   error
}

type signTickMsg struct{}

func signTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return signTickMsg{} })
}

// Init reads the stored session.
func (s *SignIn) Init() tea.Cmd {
	s.phase, s.cursor, s.message, s.input, s.started = signLoading, 0, "", "", nil
	svc, ctx := s.svc, s.ctx
	return func() tea.Msg {
		c, err := svc.Credentials(ctx)
		if err == nil && c.State() == auth.StateSignedIn {
			return signLoadedMsg{user: c.Username}
		}
		return signLoadedMsg{}
	}
}

// SetSize records the terminal size (the link and the QR code are laid out
// for it).
func (s *SignIn) SetSize(width, height int) { s.width, s.height = width, height }

func (s *SignIn) options() []string {
	if s.user != "" {
		return []string{"Keep the session of " + s.user, "Sign in again (replaces this session)"}
	}
	return []string{"Sign in to Hyper3D", s.skip}
}

// Update handles one message.
func (s *SignIn) Update(msg tea.Msg) (SignInEvent, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.SetSize(msg.Width, msg.Height)
		return SignInNone, nil
	case signLoadedMsg:
		s.user, s.phase, s.cursor = msg.user, signChoice, 0
		return SignInNone, nil
	case signTickMsg:
		if s.phase == signStarting || s.phase == signCompleting {
			s.frame++
			return SignInNone, signTick()
		}
		return SignInNone, nil
	case signStartedMsg:
		if s.phase != signStarting {
			return SignInNone, nil
		}
		if msg.err != nil {
			s.phase, s.message = signChoice, "Could not start the sign-in: "+service.Classify(msg.err).Message
			return SignInNone, nil
		}
		s.started, s.opened, s.phase, s.input, s.message = msg.started, msg.opened, signWaiting, "", ""
		s.pasting, s.copied, s.osc52 = false, "", ""
		return SignInNone, s.listen(msg.started)
	case signOutcomeMsg:
		if msg.started != s.started || (s.phase != signWaiting && s.phase != signCompleting) {
			return SignInNone, nil // an earlier or cancelled sign-in
		}
		o := msg.outcome
		if o.Err != nil {
			if errors.Is(o.Err, auth.ErrLoginReplaced) {
				return SignInNone, nil // a pasted address finished it, or it was cancelled
			}
			s.phase, s.started, s.message = signChoice, nil, service.Classify(o.Err).Message
			return SignInNone, nil
		}
		return s.finish(o.Credentials)
	case signCompletedMsg:
		if s.phase != signCompleting {
			return SignInNone, nil
		}
		if msg.err != nil {
			s.message = service.Classify(msg.err).Message
			// A wrong or stale address, or no answer from Hyper3D, used up
			// nothing: the address can be pasted again. Any other failure
			// ended this sign-in (expired, refused, code used up).
			if errors.Is(msg.err, auth.ErrNotRedirect) || errors.Is(msg.err, auth.ErrStateMismatch) || auth.IsRetryable(msg.err) {
				s.phase = signWaiting
				return SignInNone, nil
			}
			s.cancel()
			s.phase = signChoice
			return SignInNone, nil
		}
		return s.finish(msg.creds)
	case tea.KeyMsg:
		return s.key(msg)
	}
	return SignInNone, nil
}

func (s *SignIn) finish(c *auth.Credentials) (SignInEvent, tea.Cmd) {
	s.svc.AfterLogin(s.ctx, c)
	s.phase, s.started, s.message, s.input = signDone, nil, "", ""
	if c != nil {
		s.user = c.Username
	}
	return SignInNone, nil
}

// cancel ends the listener of the sign-in on screen.
func (s *SignIn) cancel() {
	if s.started != nil {
		s.svc.CancelLogin()
	}
	s.started = nil
}

// Cancel ends a sign-in in progress (the host leaves the screen).
func (s *SignIn) Cancel() { s.cancel() }

func (s *SignIn) key(k tea.KeyMsg) (SignInEvent, tea.Cmd) {
	if k.String() == "ctrl+c" {
		s.cancel()
		return SignInQuit, nil
	}
	switch s.phase {
	case signChoice:
		opts := s.options()
		switch k.String() {
		case "up", "k":
			s.cursor = (s.cursor + len(opts) - 1) % len(opts)
		case "down", "j":
			s.cursor = (s.cursor + 1) % len(opts)
		case "esc":
			return SignInBack, nil
		case "enter":
			switch {
			case s.user != "" && s.cursor == 0:
				return SignInDone, nil
			case s.user == "" && s.cursor == 1:
				return SignInSkipped, nil
			}
			return SignInNone, s.start()
		}
	case signWaiting:
		if !s.pasting {
			return s.waitingKey(k)
		}
		switch k.Type {
		case tea.KeyEsc:
			s.pasting, s.input = false, ""
			return SignInNone, nil
		case tea.KeyEnter:
			address := strings.TrimSpace(s.input)
			if address == "" {
				return SignInNone, nil
			}
			s.phase, s.message = signCompleting, ""
			svc, ctx := s.svc, s.ctx
			return SignInNone, tea.Batch(signTick(), func() tea.Msg {
				creds, err := svc.CompleteLogin(ctx, address)
				return signCompletedMsg{creds: creds, err: err}
			})
		case tea.KeyBackspace:
			if r := []rune(s.input); len(r) > 0 {
				s.input = string(r[:len(r)-1])
			}
		case tea.KeyCtrlU:
			s.input = ""
		case tea.KeyRunes, tea.KeySpace:
			s.input += string(k.Runes)
		}
	case signDone:
		if k.Type == tea.KeyEnter {
			return SignInDone, nil
		}
	}
	return SignInNone, nil
}

// waitingKey handles a key while the sign-in waits and the paste field is
// hidden: c copies the link, o opens it again, p shows the field. A paste
// opens the field with what was pasted.
func (s *SignIn) waitingKey(k tea.KeyMsg) (SignInEvent, tea.Cmd) {
	if k.Paste {
		s.pasting, s.input, s.copied = true, string(k.Runes), ""
		return SignInNone, nil
	}
	switch k.String() {
	case "esc":
		s.cancel()
		s.phase, s.message = signChoice, "Sign-in cancelled."
	case "c":
		s.message, s.osc52 = "", ""
		if err := copyText(s.started.AuthorizeURL); err != nil {
			// No clipboard tool here (a Linux without xclip, xsel or
			// wl-copy, often over SSH): ask the terminal to copy it. Many
			// terminals ignore OSC 52 and nothing can confirm it worked.
			s.osc52, s.copied = ansi.SetSystemClipboard(s.started.AuthorizeURL), "Link sent to the terminal's clipboard (not every terminal supports this)."
		} else {
			s.copied = "Link copied."
		}
	case "o":
		s.message, s.copied = "", ""
		s.opened = s.svc.OpenBrowser(s.started.AuthorizeURL) == nil
		if !s.opened {
			s.message = "No browser could be opened on this computer: press c to copy the link."
		}
	case "p":
		s.pasting, s.input, s.copied = true, "", ""
	}
	return SignInNone, nil
}

// start begins a sign-in and opens the browser of this computer.
func (s *SignIn) start() tea.Cmd {
	s.phase, s.message, s.frame = signStarting, "", 0
	svc, ctx := s.svc, s.ctx
	return tea.Batch(signTick(), func() tea.Msg {
		res, started, err := svc.Login(ctx, service.LoginInput{OpenBrowser: true})
		if err != nil {
			return signStartedMsg{err: err}
		}
		return signStartedMsg{started: started, opened: service.BrowserOpened(res)}
	})
}

// listen waits for the loopback listener's one outcome.
func (s *SignIn) listen(started *auth.Started) tea.Cmd {
	if started == nil || started.Done == nil {
		return nil
	}
	done := started.Done
	return func() tea.Msg { return signOutcomeMsg{started: started, outcome: <-done} }
}

// Hints are the keys of the current phase.
func (s *SignIn) Hints() []tui.Hint {
	switch s.phase {
	case signChoice:
		return []tui.Hint{{Key: "↑↓", Label: "move"}, {Key: "enter", Label: "select"}, {Key: "esc", Label: "back"}}
	case signWaiting:
		if s.pasting {
			return []tui.Hint{{Key: "enter", Label: "check address"}, {Key: "esc", Label: "hide field"}}
		}
		return []tui.Hint{{Key: "c", Label: "copy link"}, {Key: "o", Label: "open again"}, {Key: "p", Label: "paste address"}, {Key: "esc", Label: "cancel"}}
	case signDone:
		return []tui.Hint{{Key: "enter", Label: "continue"}}
	}
	return nil
}

// View renders the screen without chrome.
func (s *SignIn) View() string {
	theme := tui.DefaultTheme
	st := theme.Styles()
	var b strings.Builder
	switch s.phase {
	case signLoading:
		b.WriteString("  Reading the stored session...\n")
	case signChoice:
		if s.user != "" {
			fmt.Fprintf(&b, "  Signed in to Hyper3D as %s.\n\n", s.user)
		} else {
			b.WriteString("  rodin3d-mcp works on your Hyper3D account.\n\n")
		}
		b.WriteString(tui.ActionMenu(theme, s.cursor, s.options()...))
	case signStarting:
		fmt.Fprintf(&b, "  %s Starting the sign-in...\n", tui.SpinFrame(s.frame))
	case signWaiting, signCompleting:
		s.viewWaiting(&b)
	case signDone:
		fmt.Fprintf(&b, "  %s\n", st.On.Render("Signed in as "+s.user+"."))
	}
	if s.message != "" {
		// Wrapped: the terminal cuts a line longer than its width.
		b.WriteString("\n")
		for _, line := range tui.Wrap(s.message, s.width-4) {
			b.WriteString(st.Err.Render("  "+line) + "\n")
		}
	}
	if s.phase == signWaiting || s.phase == signCompleting {
		s.viewQR(&b)
	}
	if hints := s.Hints(); len(hints) > 0 {
		b.WriteString("\n" + tui.Footer(theme, tui.Hints(theme, hints...)) + "\n")
	}
	return b.String()
}

// viewWaiting is the waiting screen: what to do, the confirmation of c, and
// the paste field once p showed it. The link itself is never printed here;
// one line names the command that prints it.
func (s *SignIn) viewWaiting(b *strings.Builder) {
	st := tui.DefaultTheme.Styles()
	var lead string
	switch {
	case !s.opened:
		lead = "Press c to copy the sign-in link, open it on any device and click Authorize, then p to paste the address it ends on."
	case s.started.Listening:
		lead = "A browser tab opened on this computer: click Authorize there."
	default:
		lead = "A browser tab opened on this computer: click Authorize there, then press p to paste the address it ends on."
	}
	s.para(b, lead)
	if s.opened {
		s.para(b, "Signing in on another device? Press c to copy the link, then p to paste the address it ends on.")
	}
	// The way out when no copy reaches a clipboard: the command prints it.
	b.WriteString(st.Dim.Render("  No browser? `rodin3d-mcp login` prints the link.") + "\n")
	if s.copied != "" {
		// The OSC 52 sequence of a copy through the terminal rides on this
		// line: it has no width, and the terminal acts on it once shown.
		b.WriteString("\n" + s.osc52 + st.On.Render("  "+s.copied) + "\n")
	}
	switch {
	case s.phase == signCompleting:
		fmt.Fprintf(b, "\n  %s Checking the address...\n", tui.SpinFrame(s.frame))
	case s.pasting:
		b.WriteString("\n")
		s.para(b, "Paste the address the browser ends on (the page that cannot connect to 127.0.0.1) and press enter:")
		field := st.Dim.Render("paste the address")
		if s.input != "" {
			field = tail(s.input, s.width-8)
		}
		b.WriteString("  " + st.Cursor.Render(">") + " " + field + "_\n")
	}
}

// para writes text wrapped to the terminal's width, indented two columns.
func (s *SignIn) para(b *strings.Builder, text string) {
	width := s.width - 4
	if width <= 0 {
		width = 76
	}
	for _, line := range tui.Wrap(text, width) {
		b.WriteString("  " + line + "\n")
	}
}

// signChrome is the rows around the screen's own lines: the host's header
// and section title, the footer and the blank lines between them.
const signChrome = 8

// viewQR adds the QR code of the link under "or scan:" when the terminal
// fits it below the lines already written.
func (s *SignIn) viewQR(b *strings.Builder) {
	qr := service.QRText(s.started.AuthorizeURL)
	used := lineCount(b.String()) + signChrome + 2 // "or scan:" and its blank line
	if qr == "" || !fits(qr, s.width, s.height-used) {
		return
	}
	b.WriteString("\n  or scan:\n" + qr + "\n")
}

func lineCount(s string) int { return strings.Count(s, "\n") + 1 }

func textWidth(s string) int {
	w := 0
	for _, line := range strings.Split(s, "\n") {
		w = max(w, utf8.RuneCountInString(line))
	}
	return w
}

// fits reports whether s fits in width columns and height rows; an unknown
// size never fits.
func fits(s string, width, height int) bool {
	return width > 0 && height > 0 && textWidth(s) <= width && lineCount(s) <= height
}

// tail keeps the end of s that fits in n runes, marking a cut.
func tail(s string, n int) string {
	r := []rune(s)
	if n <= 1 || len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n+1:])
}
