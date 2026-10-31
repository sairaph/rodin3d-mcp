package guide

import (
	"encoding/json"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// topics are the topics of the guide, sorted.
var topics = []string{"credits", "inputs", "other-modes", "privacy-and-errors", "settings", "staged-flow", "start", "viewing"}

func TestSkillCarriesItsFrontmatter(t *testing.T) {
	text := SkillMarkdown()
	front, body := splitFrontmatter(text)
	for _, want := range []string{"name: " + Name + "\n", "description: ", "Use when", "  " + Generator, "  version: "} {
		if !strings.Contains(front+"\n", want) {
			t.Errorf("frontmatter lacks %q:\n%s", want, front)
		}
	}
	if strings.TrimSpace(body) == "" || Body() != body {
		t.Errorf("Body() = %q, want the text after the frontmatter", Body())
	}
	if !IsOurs(text) {
		t.Error("the embedded skill is not recognised as ours")
	}
	if n := len(text); n > 4096 {
		t.Errorf("SKILL.md is %d bytes, want under 4 KB", n)
	}
}

func TestIsOursReadsOnlyTheFrontmatter(t *testing.T) {
	cases := []struct {
		name, text string
		want       bool
	}{
		{"marker in the frontmatter", "---\nname: x\nmetadata:\n  " + Generator + "\n---\nbody\n", true},
		{"crlf line endings", "---\r\nname: x\r\nmetadata:\r\n  " + Generator + "\r\n---\r\nbody\r\n", true},
		{"marker only in the body", "---\nname: x\n---\n  " + Generator + "\n", false},
		{"no frontmatter", "  " + Generator + "\n", false},
		{"another generator", "---\nname: x\nmetadata:\n  generator: someone-else\n---\n", false},
	}
	for _, c := range cases {
		if got := IsOurs(c.text); got != c.want {
			t.Errorf("%s: IsOurs = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTopicsAndTopic(t *testing.T) {
	fsys := fstest.MapFS{
		"SKILL.md":    {Data: []byte("skill")},
		"credits.md":  {Data: []byte("credits text\r\nmore\r\n")},
		"inputs.md":   {Data: []byte("inputs text")},
		"notes.txt":   {Data: []byte("not a topic")},
		"sub/deep.md": {Data: []byte("deep")},
	}
	if got := strings.Join(topicsIn(fsys), ","); got != "credits,inputs" {
		t.Errorf("topics = %q, want credits,inputs", got)
	}
	if got, ok := topicIn(fsys, "credits"); !ok || got != "credits text\nmore" {
		t.Errorf("credits = %q, %v", got, ok)
	}
	for _, bad := range []string{"", "SKILL", "nope", "../credits", "sub/deep", "credits.md", `a\b`} {
		if _, ok := topicIn(fsys, bad); ok {
			t.Errorf("topic %q was served", bad)
		}
	}
}

func TestEveryTopicExistsAndIsWellFormed(t *testing.T) {
	if got := Topics(); strings.Join(got, ",") != strings.Join(topics, ",") {
		t.Fatalf("topics = %v\nwant    %v", got, topics)
	}
	skill := SkillMarkdown()
	for _, name := range topics {
		text, ok := Topic(name)
		if !ok || strings.TrimSpace(text) == "" {
			t.Errorf("Topic(%q) = %d bytes, %v", name, len(text), ok)
			continue
		}
		first, _, _ := strings.Cut(text, "\n")
		if !strings.HasPrefix(first, "# ") || len(first) < 4 {
			t.Errorf("%s: first line %q is not a title", name, first)
		}
		if len(text) > 6600 {
			t.Errorf("%s: %d bytes, want about 6 KB at most", name, len(text))
		}
		if !strings.Contains(skill, "`"+name+"`") {
			t.Errorf("SKILL.md does not list topic %q", name)
		}
	}
}

// topicRef is a topic named in backticks after the word topic.
var topicRef = regexp.MustCompile("[Tt]opic `([a-z-]+)`")

func TestTopicReferencesExist(t *testing.T) {
	known := map[string]bool{}
	for _, n := range topics {
		known[n] = true
	}
	err := fs.WalkDir(FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(FS(), path)
		if err != nil {
			return err
		}
		for _, m := range topicRef.FindAllStringSubmatch(string(data), -1) {
			if !known[m[1]] {
				t.Errorf("%s names topic %q, which does not exist", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoEmOrEnDashesAnywhere(t *testing.T) {
	bad := string([]rune{0x2013, 0x2014})
	err := fs.WalkDir(FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(FS(), path)
		if err != nil {
			return err
		}
		if strings.ContainsAny(string(data), bad) {
			t.Errorf("%s contains an em or en dash", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var jsonBlock = regexp.MustCompile("(?s)```json\n(.*?)\n```")

// The mcpserver tests check the examples' keys against the tool schemas.
func TestJSONExamplesAreValid(t *testing.T) {
	n := 0
	err := fs.WalkDir(FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(FS(), path)
		if err != nil {
			return err
		}
		for _, m := range jsonBlock.FindAllStringSubmatch(strings.ReplaceAll(string(data), "\r\n", "\n"), -1) {
			n++
			var v map[string]any
			if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
				t.Errorf("%s: invalid JSON example: %v\n%s", path, err, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 6 {
		t.Errorf("only %d JSON examples, want the workflows to carry calls", n)
	}
}
