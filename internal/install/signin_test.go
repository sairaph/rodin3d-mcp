package install

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/fakehyper"
	"github.com/sairaph/rodin3d-mcp/internal/service"
)

// run executes cmd and returns the first message that is not a tick,
// running the commands of a batch in order.
func run(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if c == nil {
				continue
			}
			if m := c(); m != nil {
				if _, tick := m.(signTickMsg); !tick {
					return m
				}
			}
		}
		t.Fatal("the batch made no message")
		return nil
	default:
		return msg
	}
}

func key(k tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: k} }

// startSignIn opens the screen on a signed-out account and starts a
// sign-in; the service has no browser opener, so no browser opens.
func startSignIn(t *testing.T) (*SignIn, *fakehyper.Server) {
	t.Helper()
	fake := fakehyper.New()
	t.Cleanup(fake.Close)
	svc := service.New(service.Options{Paths: domain.PathsUnder(t.TempDir()), BaseURL: fake.Base(), Version: "test"})
	t.Cleanup(svc.Close)
	s := NewSignIn(context.Background(), svc, "Skip")
	s.SetSize(120, 80)
	s.Update(run(t, s.Init()))
	if s.phase != signChoice || s.User() != "" {
		t.Fatalf("phase %d user %q, want the choice of a signed-out account", s.phase, s.User())
	}
	_, cmd := s.Update(key(tea.KeyEnter))
	s.Update(run(t, cmd))
	if s.phase != signWaiting || s.started == nil {
		t.Fatalf("phase %d, message %q: the sign-in did not start", s.phase, s.message)
	}
	t.Cleanup(s.Cancel)
	return s, fake
}

func finish(t *testing.T, s *SignIn, fake *fakehyper.Server) {
	t.Helper()
	if s.phase != signDone || s.User() != fake.User.Username {
		t.Fatalf("phase %d user %q message %q, want signed in as %q", s.phase, s.User(), s.message, fake.User.Username)
	}
	if v := s.View(); !strings.Contains(v, "Signed in as "+fake.User.Username+".") || !strings.Contains(v, "enter continue") || strings.Count(v, "\n") > 4 {
		t.Fatalf("done screen:\n%s", v)
	}
	if ev, _ := s.Update(key(tea.KeyEnter)); ev != SignInDone {
		t.Fatalf("enter on the done screen: event %d", ev)
	}
}

// The two ways D20 finishes a sign-in: the pasted final address (any
// device), and the loopback listener (the browser of this computer).
func TestSignInByPastedAddress(t *testing.T) {
	s, fake := startSignIn(t)
	address, err := fake.Approve(s.started.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	// The field is hidden until p; text that is not the final address keeps
	// the screen waiting with the reason.
	if strings.Contains(s.View(), "paste the address_") {
		t.Fatal("the paste field shows before p")
	}
	s.Update(runes("p"))
	if !s.pasting || !strings.Contains(s.View(), "paste the address_") {
		t.Fatalf("p did not show the field:\n%s", s.View())
	}
	s.Update(runes("not an address"))
	_, cmd := s.Update(key(tea.KeyEnter))
	s.Update(run(t, cmd))
	if s.phase != signWaiting || s.message == "" {
		t.Fatalf("phase %d message %q after a wrong paste, want waiting with the reason", s.phase, s.message)
	}
	s.Update(key(tea.KeyCtrlU))
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(address), Paste: true})
	_, cmd = s.Update(key(tea.KeyEnter))
	s.Update(run(t, cmd))
	finish(t, s, fake)
}

// A paste while the field is hidden opens it with the pasted text; esc in
// the field hides it again without cancelling the sign-in.
func TestSignInPasteOpensField(t *testing.T) {
	s, _ := startSignIn(t)
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("http://127.0.0.1:1/x"), Paste: true})
	if !s.pasting || s.input != "http://127.0.0.1:1/x" {
		t.Fatalf("pasting %v input %q", s.pasting, s.input)
	}
	s.Update(key(tea.KeyEsc))
	if s.pasting || s.phase != signWaiting || s.started == nil {
		t.Fatalf("esc in the field: pasting %v phase %d", s.pasting, s.phase)
	}
	s.Update(key(tea.KeyEsc))
	if s.phase != signChoice || s.message != "Sign-in cancelled." {
		t.Fatalf("esc on the waiting screen: phase %d message %q", s.phase, s.message)
	}
}

// The waiting screen never prints the link: c copies it (the system
// clipboard, or OSC 52 through the terminal when there is none).
func TestSignInCopiesTheLink(t *testing.T) {
	var copied []string
	copyErr := error(nil)
	prev := copyText
	copyText = func(text string) error { copied = append(copied, text); return copyErr }
	t.Cleanup(func() { copyText = prev })

	s, _ := startSignIn(t)
	link := s.started.AuthorizeURL
	view := s.View()
	for _, want := range []string{"Press c to copy the sign-in link", "No browser? `rodin3d-mcp login` prints the link.", "c copy link", "o open again", "p paste address", "esc cancel"} {
		if !strings.Contains(view, want) {
			t.Errorf("the waiting screen lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, link) || strings.Contains(view, "Link copied") {
		t.Fatalf("the waiting screen prints the link or a copy before c:\n%s", view)
	}
	s.Update(runes("c"))
	if len(copied) != 1 || copied[0] != link || !strings.Contains(s.View(), "Link copied.") {
		t.Fatalf("copied %v; view:\n%s", copied, s.View())
	}
	copyErr = errors.New("no clipboard utilities")
	s.Update(runes("c"))
	if want := ansi.SetSystemClipboard(link); !strings.Contains(s.View(), want) ||
		!strings.Contains(s.View(), "Link sent to the terminal's clipboard (not every terminal supports this).") || strings.Contains(s.View(), "Link copied") {
		t.Fatalf("no unconfirmed OSC 52 copy after a clipboard failure:\n%q", s.View())
	}
	s.Update(runes("p"))
	if strings.Contains(s.View(), "Link copied") {
		t.Fatal("the copy confirmation stays once the field opens")
	}
}

// The QR code shows under "or scan:" only when the terminal fits it.
func TestSignInQRWhenItFits(t *testing.T) {
	s, _ := startSignIn(t)
	s.SetSize(120, 40)
	if strings.Contains(s.View(), "or scan:") {
		t.Fatalf("a QR code in 120x40:\n%s", s.View())
	}
	qr := service.QRText(s.started.AuthorizeURL)
	s.SetSize(textWidth(qr), lineCount(qr)+40)
	if v := s.View(); !strings.Contains(v, "or scan:\n"+qr) {
		t.Fatalf("no QR code in %dx%d:\n%s", s.width, s.height, v)
	}
}

// With the browser of this computer opened, the screen says to click
// Authorize there and how to sign in on another device.
func TestSignInOpenedText(t *testing.T) {
	s, _ := startSignIn(t)
	s.opened = true
	s.started.Listening = true
	want := "  A browser tab opened on this computer: click Authorize there.\n" +
		"  Signing in on another device? Press c to copy the link, then p to paste the address it ends on.\n"
	if v := s.View(); !strings.HasPrefix(v, want) {
		t.Fatalf("view:\n%s\nwant it to start with:\n%s", v, want)
	}
	s.started.Listening = false
	if v := s.View(); !strings.Contains(v, "click Authorize there, then press p to paste the address it ends on.") {
		t.Fatalf("without a listener:\n%s", v)
	}
}

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestSignInByLoopback(t *testing.T) {
	s, fake := startSignIn(t)
	if !s.started.Listening {
		t.Skip("every sign-in port is busy on this machine")
	}
	address, err := fake.Approve(s.started.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	listened := make(chan tea.Msg, 1)
	go func() { listened <- s.listen(s.started)() }()
	resp, err := http.Get(address) // the browser following the redirect
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback answered %d", resp.StatusCode)
	}
	s.Update(<-listened)
	finish(t, s, fake)
}
