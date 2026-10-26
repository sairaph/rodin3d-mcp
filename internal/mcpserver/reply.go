package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/service"
)

// replyMargin is room kept in a reply for its JSON framing.
const replyMargin = 32 << 10

// maxMessageBytes bounds an error message.
const maxMessageBytes = 2 << 10

func successResult(front any, body string) *mcp.CallToolResult {
	return render.SuccessResult(front, body)
}

func shortMessage(s string) string {
	if len(s) <= maxMessageBytes {
		return s
	}
	cut := maxMessageBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " ... (message truncated)"
}

// reply renders a service result, attaching its images as optional
// feedback, or the classified error.
func reply(res service.Result, err error) *mcp.CallToolResult {
	if err != nil {
		e := service.Classify(err)
		e.Message = shortMessage(e.Message)
		return render.ErrorResult(e)
	}
	out := successResult(res.Front, res.Body)
	if out.IsError {
		return out
	}
	for _, img := range res.Images {
		out = attachImage(out, img.PNG, img.What)
	}
	return out
}

// imageReply renders a result whose point is its one image: an image that
// cannot be attached is an error.
func imageReply(res service.Result, err error) *mcp.CallToolResult {
	if err != nil || len(res.Images) == 0 {
		return reply(res, err)
	}
	return imageResult(res.Front, res.Body, res.Images[0].PNG)
}

// invalidArguments turns the SDK's argument validation failures into an
// invalid_input result with a hint, and treats missing arguments as {}.
func invalidArguments(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if call, ok := req.(*mcp.CallToolRequest); ok && method == "tools/call" && call.Params != nil {
			if a := strings.TrimSpace(string(call.Params.Arguments)); a == "" || a == "null" {
				call.Params.Arguments = json.RawMessage("{}")
			}
		}
		result, err := next(ctx, method, req)
		if err != nil || method != "tools/call" {
			return result, err
		}
		res, ok := result.(*mcp.CallToolResult)
		if !ok || res == nil || !res.IsError || res.GetError() == nil {
			return result, err
		}
		tool := "the tool"
		if call, ok := req.(*mcp.CallToolRequest); ok && call.Params != nil && call.Params.Name != "" {
			tool = call.Params.Name
		}
		fresh := render.ErrorResult(render.Error{
			Code:    render.CodeInvalidInput,
			Message: shortMessage("Invalid arguments: " + argumentProblem(res.GetError())),
			Hint: strings.TrimSpace(fmt.Sprintf("Call %s again with arguments that match its input schema: every required "+
				"argument, each of the listed type, and only listed values and argument names. %s", tool, argumentsOf(tool))),
		})
		// Change the result in place: the SDK has already marked it complete.
		res.Content, res.StructuredContent, res.IsError = fresh.Content, fresh.StructuredContent, true
		return res, nil
	}
}

func argumentProblem(err error) string {
	msg := err.Error()
	for _, prefix := range []string{`validating "arguments": `, "validating root: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return strings.TrimPrefix(msg, "json: ")
}

// toolArguments maps a tool to the sentence listing its arguments.
var toolArguments sync.Map

func rememberArguments(name string, schema *jsonschema.Schema) {
	required := map[string]bool{}
	for _, r := range schema.Required {
		required[r] = true
	}
	var names []string
	for prop := range schema.Properties {
		if required[prop] {
			prop += " (required)"
		}
		names = append(names, prop)
	}
	sort.Strings(names)
	if len(names) == 0 {
		toolArguments.Store(name, name+" takes no arguments.")
		return
	}
	toolArguments.Store(name, name+" takes: "+strings.Join(names, ", ")+".")
}

func argumentsOf(tool string) string {
	if v, ok := toolArguments.Load(tool); ok {
		return v.(string)
	}
	return ""
}

// recovering turns a panic in a handler into an internal_error result.
func recovering[In any](name string, h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error)) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (res *mcp.CallToolResult, out any, err error) {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "%s: %s panicked: %v\n%s", domain.BinaryName, name, r, debug.Stack())
				res, out, err = render.ErrorResult(render.Error{
					Code:    render.CodeInternal,
					Message: fmt.Sprintf("%s failed unexpectedly: %v", name, r),
					Hint:    "This is a bug, not caused by the arguments. Run `" + domain.BinaryName + " doctor`, then retry.",
				}), nil, nil
			}
		}()
		return h(ctx, req, in)
	}
}
