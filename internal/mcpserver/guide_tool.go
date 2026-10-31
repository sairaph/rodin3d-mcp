package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/guide"
)

// guideTool reads the embedded guide: no account, no network.
var guideTool = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}

type guideInput struct {
	Topic *string `json:"topic,omitempty" jsonschema:"topic name from the index (default: list the topics)"`
}

type guideFront struct {
	Topic  string   `yaml:"topic,omitempty"`
	Topics []string `yaml:"topics,omitempty"`
}

// topicSummaries are the one-line descriptions listed in the guide index.
var topicSummaries = map[string]string{
	"start":              "account, sign-in with the link and QR code, permissions",
	"credits":            "quote_cost, expected_credits, timeouts, free operations, frozen credits",
	"inputs":             "images, view tags, weights, input sets from the viewer",
	"settings":           "models, tiers, modes, recipes and every stage's settings",
	"staged-flow":        "draft, redo, confirm, texture, export, download, fork, waiting",
	"other-modes":        "Agentic builds, parts, rigging, animation, texturing your own model",
	"viewing":            "get_preview modes and the viewer window",
	"privacy-and-errors": "private tasks and what to do for each error code",
}

func (s *Server) registerGuideTool() {
	addTool(s, "get_guide", "Usage guide",
		"Read the guide to these tools: short rules and workflows for turning images into 3D models with Rodin. Free, works "+
			"without signing in. Without arguments it lists the topics. Pass topic for a page: start (sign-in, permissions), credits "+
			"(quote_cost, expected_credits, timeouts, frozen credits), inputs (images, view tags, weights, input sets), settings "+
			"(models, tiers, recipes, stage settings), staged-flow (draft to download), other-modes (Agentic, parts, rigging, "+
			"animation, your own model), viewing (previews and the viewer window), privacy-and-errors. The same pages are installed "+
			"as the rodin3d skill.",
		guideTool, nonNull(inputSchema[guideInput](nil)), s.getGuide)
}

func (s *Server) getGuide(_ context.Context, _ *mcp.CallToolRequest, in guideInput) (*mcp.CallToolResult, any, error) {
	topic := strings.ToLower(strings.TrimSpace(optString(in.Topic)))
	names := guide.Topics()
	if topic == "" {
		var b strings.Builder
		b.WriteString("Guide topics (call get_guide with {\"topic\": \"<name>\"}); start with `start`:\n\n")
		for _, n := range names {
			fmt.Fprintf(&b, "- `%s`: %s\n", n, topicSummaries[n])
		}
		return successResult(guideFront{Topics: names}, strings.TrimRight(b.String(), "\n")), nil, nil
	}
	text, ok := guide.Topic(topic)
	if !ok {
		return render.ErrorResult(render.Error{
			Code:    render.CodeNotFound,
			Message: fmt.Sprintf("No guide topic %q.", topic),
			Hint:    "Call get_guide with no arguments to list the topics: " + strings.Join(names, ", ") + ".",
		}), nil, nil
	}
	return successResult(guideFront{Topic: topic}, text), nil, nil
}
