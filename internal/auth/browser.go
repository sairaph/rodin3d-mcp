package auth

import (
	"errors"
	"os/exec"
	"runtime"
)

// OpenBrowser asks the operating system to open address in the user's
// default browser. It never waits for the browser and never fails a sign-in:
// the caller shows the address either way.
func OpenBrowser(address string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	case "darwin":
		cmd = exec.Command("open", address)
	case "linux", "freebsd", "openbsd", "netbsd":
		cmd = exec.Command("xdg-open", address)
	default:
		return errors.New("no known way to open a browser on " + runtime.GOOS)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
