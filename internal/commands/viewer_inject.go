//go:build viewertest

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sairaph/mcp-wizard/command"

	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// viewer-inject sends scripted mouse and key events into the open viewer
// window (test builds only): viewer-inject '<json list of events>' or
// viewer-inject @<file holding the list>. It
// prints the widgets on screen and the state after the events, as JSON.
func init() {
	extraCommands = append(extraCommands, func(reg *command.Registry, env Env) {
		reg.Register(command.Handler{Name: "viewer-inject", Description: "Test builds: send mouse and key events to the viewer window",
			Usage: `viewer-inject '[{"type":"click","target":"mode:glossy"}, ...]'`,
			Run: func(ctx context.Context, raw []string) int {
				svc := env.Service()
				defer svc.Close()
				var p viewer.InjectParams
				data := []byte(strings.Join(raw, " "))
				if len(raw) == 1 && strings.HasPrefix(raw[0], "@") {
					b, err := os.ReadFile(raw[0][1:])
					if err != nil {
						fmt.Fprintf(env.Stderr, "Error: %v\n", err)
						return 2
					}
					data = b
				}
				if err := json.Unmarshal(data, &p.Events); err != nil {
					fmt.Fprintf(env.Stderr, "Error: events must be a JSON list: %v\n", err)
					return 2
				}
				ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
				defer cancel()
				var rep viewer.InjectReply
				if err := viewer.Call(ctx, svc.Paths(), viewer.MethodInject, p, &rep); err != nil {
					fmt.Fprintf(env.Stderr, "Error: %v\n", err)
					return 1
				}
				out, _ := json.MarshalIndent(rep, "", "  ")
				fmt.Fprintln(env.Stdout, string(out))
				if rep.Error != nil {
					return 1
				}
				return 0
			}})
	})
}
