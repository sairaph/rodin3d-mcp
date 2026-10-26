//go:build !viewertest

package app

import "github.com/sairaph/mcp-wizard/daemon/socket"

// Release builds take input from the mouse and keys only; inject_input is
// compiled with the viewertest tag (inject_on.go).

type testHooks struct{}

func (a *App) injected() (frameInput, bool) { return frameInput{}, false }

func registerTestMethods(*App, *socket.Server) {}
