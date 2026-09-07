package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The skills ship as a Claude Code plugin whose root is ./cmd/code-graph/assets
// -- the SAME directory the go:embed directives in assets.go read. That is
// deliberate: there is no generated second copy of the SKILL.md files, so the
// embedded CLI assets and the published plugin cannot drift apart. These tests
// hold that property in place, because the obvious "tidy-up" refactor is to
// give the plugin its own copied tree, and nothing else would notice.

const (
	marketplacePath = "../../.claude-plugin/marketplace.json"
	pluginRootRel   = "assets"
	pluginJSONPath  = "assets/.claude-plugin/plugin.json"
)

type marketplaceDoc struct {
	Name    string `json:"name"`
	Plugins []struct {
		Name    string `json:"name"`
		Source  string `json:"source"`
		Version string `json:"version"`
	} `json:"plugins"`
}

func readMarketplace(t *testing.T) marketplaceDoc {
	t.Helper()
	data, err := os.ReadFile(marketplacePath)
	if err != nil {
		t.Fatalf("read %s: %v", marketplacePath, err)
	}
	var doc marketplaceDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", marketplacePath, err)
	}
	if len(doc.Plugins) == 0 {
		t.Fatalf("%s declares no plugins; every check below would be vacuous", marketplacePath)
	}
	return doc
}

// TestPluginSourceIsTheEmbeddedAssetsDirectory fails if the plugin is ever
// repointed at a copied tree.
func TestPluginSourceIsTheEmbeddedAssetsDirectory(t *testing.T) {
	doc := readMarketplace(t)
	const want = "./cmd/code-graph/assets"
	found := false
	for _, p := range doc.Plugins {
		if p.Source == want {
			found = true
		}
	}
	if !found {
		got := make([]string, 0, len(doc.Plugins))
		for _, p := range doc.Plugins {
			got = append(got, p.Source)
		}
		t.Fatalf("no plugin sourced from %q (got %v). The plugin root must stay the "+
			"go:embed directory so the embedded assets and the published plugin "+
			"cannot diverge; a copied tree would need a sync check that does not exist.",
			want, strings.Join(got, ", "))
	}
}

// TestEveryEmbeddedSkillIsPublishedByThePlugin ties the two surfaces together
// by MEMBERSHIP rather than by a count, so adding a fifth skill to assets.go
// without shipping it (or vice versa) fails.
func TestEveryEmbeddedSkillIsPublishedByThePlugin(t *testing.T) {
	if len(skillFiles) == 0 {
		t.Fatal("skillFiles is empty; the check would be vacuous")
	}
	for name := range skillFiles {
		path := filepath.Join(pluginRootRel, "skills", name, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("embedded skill %q has no file at %s, so the plugin does not "+
				"publish it: %v", name, path, err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(pluginRootRel, "skills"))
	if err != nil {
		t.Fatalf("read plugin skills dir: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, ok := skillFiles[e.Name()]; !ok {
			t.Errorf("plugin publishes %q but assets.go does not embed it; the CLI "+
				"and the plugin would ship different skill sets", e.Name())
		}
	}
}

// TestPluginVersionIsRecordedInTheChangelog is a floor, not a proof. Nothing in
// the repository holds the release version -- release.yml takes it as a workflow
// input and the binary is ldflags-stamped -- so plugin.json's version cannot be
// derived from a constant. This at least catches a version that was invented or
// typo'd, and forces the CHANGELOG entry that a plugin bump needs.
func TestPluginVersionIsRecordedInTheChangelog(t *testing.T) {
	data, err := os.ReadFile(pluginJSONPath)
	if err != nil {
		t.Fatalf("read %s: %v", pluginJSONPath, err)
	}
	var plugin struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &plugin); err != nil {
		t.Fatalf("parse %s: %v", pluginJSONPath, err)
	}
	if plugin.Version == "" {
		t.Fatal("plugin.json declares no version")
	}

	changelog, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}
	if !strings.Contains(string(changelog), plugin.Version) {
		t.Errorf("plugin.json version %q appears nowhere in CHANGELOG.md; a plugin "+
			"version bump needs its own changelog entry", plugin.Version)
	}

	// The marketplace entry and the plugin manifest must agree.
	doc := readMarketplace(t)
	for _, p := range doc.Plugins {
		if p.Name == plugin.Name && p.Version != plugin.Version {
			t.Errorf("marketplace lists %s at %q but plugin.json says %q",
				p.Name, p.Version, plugin.Version)
		}
	}
}
