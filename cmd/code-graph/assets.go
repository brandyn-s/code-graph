package main

import "embed"

//go:embed assets/skills/code-graph-exploring/SKILL.md
var skillExploring string

//go:embed assets/skills/code-graph-tracing/SKILL.md
var skillTracing string

//go:embed assets/skills/code-graph-quality/SKILL.md
var skillQuality string

//go:embed assets/skills/code-graph-reference/SKILL.md
var skillReference string

//go:embed assets/codex-instructions.md
var codexInstructions string

// skillFiles maps skill directory name to embedded content.
var skillFiles = map[string]string{
	"code-graph-exploring": skillExploring,
	"code-graph-tracing":   skillTracing,
	"code-graph-quality":   skillQuality,
	"code-graph-reference": skillReference,
}

// Ensure embed import is used.
var _ embed.FS
