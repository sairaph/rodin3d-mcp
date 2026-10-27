package install

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sairaph/mcp-wizard/tui"

	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/service"
)

// SignIn is the Hyper3D sign-in screen of the installer and the app (D20):
// it starts a sign-in, opens the browser of this computer, and shows the
// link and a QR code of it for any other device. The loopback listener
// finishes the sign-in when the browser runs here; otherwise the user pastes
// the final address (the page that cannot connect to 127.0.0.1).
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
	input   string
	message string
	frame   int
	width   int
	height  int
}

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
		switch k.Type {
		case tea.KeyEsc:
			s.cancel()
			s.phase, s.message = signChoice, "Sign-in cancelled."
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
			if k.String() == "o" && s.input == "" && !k.Paste {
				// o on an empty field opens the link again.
				s.opened = s.svc.OpenBrowser(s.started.AuthorizeURL) == nil
				return SignInNone, nil
			}
			s.input += string(k.Runes)
		}
	case signDone:
		if k.Type == tea.KeyEnter {
			return SignInDone, nil
		}
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
		return []tui.Hint{{Key: "enter", Label: "use the pasted address"}, {Key: "o", Label: "open the link again"}, {Key: "esc", Label: "cancel"}}
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
			b.WriteString("  rodin3d-mcp works on your Hyper3D account: sign in with the browser on\n")
			b.WriteString("  this computer, or on any other device (phone, another computer).\n\n")
		}
		b.WriteString(tui.ActionMenu(theme, s.cursor, s.options()...))
	case signStarting:
		fmt.Fprintf(&b, "  %s Starting the sign-in...\n", tui.SpinFrame(s.frame))
	case signWaiting, signCompleting:
		s.viewWaiting(&b)
	case signDone:
		fmt.Fprintf(&b, "  %s\n", st.On.Render("Signed in to Hyper3D as "+s.user+"."))
	}
	if s.message != "" {
		// Wrapped: the terminal cuts a line longer than its width.
		b.WriteString("\n")
		for _, line := range tui.Wrap(s.message, s.width-4) {
			b.WriteString(st.Err.Render("  "+line) + "\n")
		}
	}
	if hints := s.Hints(); len(hints) > 0 {
		b.WriteString("\n" + tui.Footer(theme, tui.Hints(theme, hints...)) + "\n")
	}
	return b.String()
}

func (s *SignIn) viewWaiting(b *strings.Builder) {
	theme := tui.DefaultTheme
	st := theme.Styles()
	url := s.started.AuthorizeURL
	b.WriteString("  Open this link on any device, sign in to Hyper3D if asked, and click\n")
	b.WriteString("  Authorize on the page \"Connect rodin3d-mcp to Hyper3D task API\".\n")
	if s.opened {
		b.WriteString(st.Dim.Render("  (The browser of this computer was opened with it.)") + "\n")
	}
	b.WriteString("\n")
	// The link is cut into lines of the terminal's width with no indent: the
	// terminal would cut a longer line, and a browser's address bar drops
	// the line breaks of a pasted address.
	b.WriteString(breakLines(url, s.width) + "\n\n")

	text := 14 // lines of text besides the link and the QR code
	qr := service.QRText(url)
	switch {
	case qr == "":
	case fits(qr, s.width, s.height-text-lineCount(url, s.width)):
		b.WriteString(qr + "\n")
	default:
		w, h := textWidth(qr), lineCount(qr, 0)+text+lineCount(url, s.width)
		b.WriteString(st.Dim.Render(fmt.Sprintf("  (A terminal of at least %d columns and %d rows also shows a QR code of the link.)", w, h)) + "\n\n")
	}
	if s.started.Listening {
		b.WriteString("  If that browser runs on this computer, the sign-in finishes by itself.\n")
		b.WriteString("  Otherwise the browser ends on a page that cannot connect to 127.0.0.1:\n")
	} else {
		b.WriteString("  After Authorize the browser ends on a page that cannot connect to 127.0.0.1:\n")
	}
	b.WriteString("  paste that page's whole address here and press enter.\n\n")
	if s.phase == signCompleting {
		fmt.Fprintf(b, "  %s Checking the address...\n", tui.SpinFrame(s.frame))
	} else {
		field := st.Dim.Render("paste the final address")
		if s.input != "" {
			field = tail(s.input, s.width-8)
		}
		b.WriteString("  " + st.Cursor.Render(">") + " " + field + "_\n")
	}
	fmt.Fprintf(b, "\n%s\n", st.Dim.Render(fmt.Sprintf("  The link works until %s. Without a browser here, `%s login` does the same.",
		s.started.ExpiresAt.Local().Format("15:04"), domain.BinaryName)))
}

// breakLines cuts s into lines of width runes (all of s on one line when the
// width is unknown).
func breakLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	r := []rune(s)
	var lines []string
	for len(r) > width {
		lines = append(lines, string(r[:width]))
		r = r[width:]
	}
	lines = append(lines, string(r))
	return strings.Join(lines, "\n")
}

func lineCount(s string, width int) int {
	if width > 0 {
		s = breakLines(s, width)
	}
	return strings.Count(s, "\n") + 1
}

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
	return width > 0 && height > 0 && textWidth(s) <= width && lineCount(s, 0) <= height
}

// tail keeps the end of s that fits in n runes, marking a cut.
func tail(s string, n int) string {
	r := []rune(s)
	if n <= 1 || len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n+1:])
}
