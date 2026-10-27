package install

import (
	"context"
	"net/http"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
	// Text that is not the final address keeps the screen waiting.
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("not an address")})
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
