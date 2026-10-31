package mcpserver

import (
	"context"
	"encoding/json"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/guide"
)

func TestGetGuideListsAndServesTopicsSignedOut(t *testing.T) {
	f := newFixture(t, false, false)
	idx := f.call("get_guide", nil)
	if idx.res.IsError {
		t.Fatal(idx.text)
	}
	for _, name := range guide.Topics() {
		if !strings.Contains(idx.text, "`"+name+"`: ") || topicSummaries[name] == "" {
			t.Errorf("index lacks a summary of %q:\n%s", name, idx.text)
		}
	}
	if len(topicSummaries) != len(guide.Topics()) {
		t.Errorf("%d summaries for %d topics", len(topicSummaries), len(guide.Topics()))
	}
	page := f.call("get_guide", map[string]any{"topic": " Start "})
	if page.res.IsError || page.front["topic"] != "start" || !strings.Contains(page.text, "# Start") {
		t.Errorf("topic start: %s", page.text)
	}
	bad := f.call("get_guide", map[string]any{"topic": "../SKILL"})
	if !bad.res.IsError || bad.code() != "not_found" || !strings.Contains(bad.text, "staged-flow") {
		t.Errorf("unknown topic: %s", bad.text)
	}
}

// schemaNames collects the property names and the string enum values of a
// JSON schema, nested objects and array items included.
func schemaNames(v any, props, values map[string]bool) {
	switch s := v.(type) {
	case map[string]any:
		if p, ok := s["properties"].(map[string]any); ok {
			for name, sub := range p {
				props[name] = true
				schemaNames(sub, props, values)
			}
		}
		if e, ok := s["enum"].([]any); ok {
			for _, x := range e {
				if str, ok := x.(string); ok {
					values[str] = true
				}
			}
		}
		if items, ok := s["items"]; ok {
			schemaNames(items, props, values)
		}
	case []any:
		for _, x := range s {
			schemaNames(x, props, values)
		}
	}
}

// guideSurface is what the server offers: tool names, parameter names and
// enum values, read from the live tool list.
func guideSurface(t *testing.T) (tools, props, values map[string]bool) {
	t.Helper()
	f := newFixture(t, false, false)
	res, err := f.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, props, values = map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, tool := range res.Tools {
		tools[tool.Name] = true
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		schemaNames(schema, props, values)
	}
	return tools, props, values
}

// replyWords are the snake_case words of replies and errors the guide may
// name besides tools, parameters and enum values: reply fields, error codes
// and the stages a reply reports.
var replyWords = []string{
	// get_account, login, get_generation_options and quote_cost replies.
	"expires_at", "authorize_url", "default_weights", "charged_at", "free_reason", "frozen_at_submit",
	// get_task, the stage tools and get_viewer_state.
	"redo_left", "bang_plan", "timed_out", "user_actions",
	// Error codes and fields.
	"invalid_input", "not_found", "rate_limited", "insufficient_credits", "service_error", "internal_error", "service_code",
}

var snake = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)

// guideFiles returns every file of the guide by path, with LF line ends.
func guideFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(guide.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(guide.FS(), path)
		out[path] = strings.ReplaceAll(string(data), "\r\n", "\n")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGuideNamesOnlyRealToolsParametersAndValues(t *testing.T) {
	tools, props, values := guideSurface(t)
	known := map[string]bool{}
	for _, set := range []map[string]bool{tools, props, values} {
		for w := range set {
			known[w] = true
		}
	}
	for _, w := range replyWords {
		if known[w] {
			t.Errorf("reply word %q is a tool, parameter or value already: drop it from replyWords", w)
		}
		known[w] = true
	}
	used := map[string]bool{}
	for path, text := range guideFiles(t) {
		for _, tok := range snake.FindAllString(text, -1) {
			used[tok] = true
			if !known[tok] {
				t.Errorf("%s uses %q: not a tool, a parameter, a value or a reply word of this server", path, tok)
			}
		}
	}
	for _, w := range replyWords {
		if !used[w] {
			t.Errorf("reply word %q is not used by the guide: drop it from replyWords", w)
		}
	}
	if len(used) < 80 {
		t.Errorf("only %d distinct snake_case words checked", len(used))
	}
}

func TestGuideCoversEveryTool(t *testing.T) {
	tools, _, _ := guideSurface(t)
	var all strings.Builder
	for _, text := range guideFiles(t) {
		all.WriteString(text)
	}
	for name := range tools {
		if !regexp.MustCompile(`\b` + name + `\b`).MatchString(all.String()) {
			t.Errorf("the guide never names the tool %s", name)
		}
	}
}

var jsonBlock = regexp.MustCompile("(?s)```json\n(.*?)\n```")

// jsonKeys collects the object keys of a decoded JSON value.
func jsonKeys(v any, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, sub := range x {
			out[k] = true
			jsonKeys(sub, out)
		}
	case []any:
		for _, sub := range x {
			jsonKeys(sub, out)
		}
	}
}

func TestGuideExamplesUseRealParameters(t *testing.T) {
	_, props, values := guideSurface(t)
	n := 0
	for path, text := range guideFiles(t) {
		for _, m := range jsonBlock.FindAllStringSubmatch(text, -1) {
			n++
			var v map[string]any
			if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
				t.Errorf("%s: invalid JSON example: %v", path, err)
				continue
			}
			keys := map[string]bool{}
			jsonKeys(v, keys)
			for k := range keys {
				if !props[k] {
					t.Errorf("%s: example key %q is no parameter of any tool:\n%s", path, k, m[1])
				}
			}
			if op, ok := v["operation"].(string); ok && !values[op] {
				t.Errorf("%s: example quotes operation %q, which quote_cost does not take", path, op)
			}
		}
	}
	if n == 0 {
		t.Error("no JSON examples found")
	}
}
