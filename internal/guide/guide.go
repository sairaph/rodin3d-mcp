// Package guide holds the rodin3d skill: SKILL.md and the topic files it
// points to, embedded so the guide always matches the installed server. The
// installer writes them into AI clients' skill folders (internal/install,
// skill.go), and the get_guide tool serves the same topics to clients that
// have no skill folder.
package guide

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

// Name is the skill's name and the name of its folder.
const Name = "rodin3d"

// Generator is the frontmatter marker that says rodin3d-mcp wrote a copy of
// the skill: install replaces and uninstall removes only folders that carry
// it.
const Generator = "generator: rodin3d-mcp"

//go:embed rodin3d
var files embed.FS

// FS is the skill folder: SKILL.md and the topic files at its top level.
func FS() fs.FS {
	sub, err := fs.Sub(files, Name)
	if err != nil {
		panic(err)
	}
	return sub
}

// SkillMarkdown is the full SKILL.md, frontmatter included.
func SkillMarkdown() string {
	data, err := fs.ReadFile(FS(), "SKILL.md")
	if err != nil {
		panic(err)
	}
	return string(data)
}

// splitFrontmatter splits a SKILL.md into its YAML frontmatter and its body;
// the frontmatter is empty when the text has none.
func splitFrontmatter(text string) (frontmatter, body string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if rest, ok := strings.CutPrefix(text, "---\n"); ok {
		if front, body, found := strings.Cut(rest, "\n---\n"); found {
			return front, strings.TrimLeft(body, "\n")
		}
	}
	return "", text
}

// Body is SKILL.md without its frontmatter.
func Body() string {
	_, body := splitFrontmatter(SkillMarkdown())
	return body
}

// IsOurs reports whether the SKILL.md text was written by rodin3d-mcp: its
// frontmatter, not its body, holds the marker as a metadata line.
func IsOurs(skillMarkdown string) bool {
	front, _ := splitFrontmatter(skillMarkdown)
	for _, line := range strings.Split(front, "\n") {
		if line == "  "+Generator {
			return true
		}
	}
	return false
}

// Topics lists the guide's topic files by name (file name without .md),
// sorted, excluding SKILL.md. The get_guide tool serves the same list.
func Topics() []string { return topicsIn(FS()) }

// Topic returns the text of one topic by name, and false for an unknown name.
func Topic(name string) (string, bool) { return topicIn(FS(), name) }

func topicsIn(fsys fs.FS) []string {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		panic(err)
	}
	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".md"); ok && !e.IsDir() && e.Name() != "SKILL.md" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func topicIn(fsys fs.FS, name string) (string, bool) {
	// Only a bare topic name: never a path.
	if name == "" || name == "SKILL" || strings.ContainsAny(name, `/\.`) {
		return "", false
	}
	data, err := fs.ReadFile(fsys, name+".md")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")), true
}
