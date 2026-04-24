// Package pipeline: Nix service extraction.
//
// Scans Nix module files (nix/modules/*.nix and similar) for redacted
// service declarations that define pub/sub topic bindings:
//
//   options.redacted.services.<name> = {
//     baf.pub_topic = mkOption { default = "<topic>"; };
//     baf.sub_topics = mkOption { default = [ "a" "b" ]; };
//   };
//
// and for imperative topic declarations in systemd service scripts:
//
//   ${pkgs.redacted.pubmsg}/bin/pubmsg <topic>
//   ${pkgs.redacted.submsg}/bin/submsg <topic1> <topic2>
//
// Emits Service nodes + PUBLISHES_TO / SUBSCRIBES_TO edges connecting
// Services to Topic nodes (unified with Zenoh's Topic label).
//
// WHY this pass complements Zenoh extraction:
//   libio-ng services don't put topics in Rust code — they're routed by
//   the Nix module system via pubmsg/submsg pipelines. This pass surfaces
//   the declarative pub/sub graph that Zenoh regex extraction can't see.
//
// MVP scope (v0):
//   - `options.redacted.services.<name>` declarations → Service nodes
//   - `baf.pub_topic = mkOption { default = "<literal>"; }` → PUBLISHES_TO
//   - `baf.sub_topics = mkOption { default = [ "a" "b" ]; }` → SUBSCRIBES_TO
//   - `redacted.services.<name>.additional_sub_topics = [ "X" ]` → SUBSCRIBES_TO
//   - `pubmsg <topic>` / `submsg <topics>` in script blocks → PUBLISHES_TO/SUBSCRIBES_TO
//
// Deferred:
//   - Conditional appends (`++ (if x then [...] else [])`) — MVP captures base list only
//   - Topic variable resolution (`default = cfg.baf.pub_topic`)
//   - RUNS_BINARY edges (Service → Rust binary via pkgs.redacted.<name>)
//   - Nix flake output resolution
//   - Tree-sitter-based AST walking (regex is sufficient for these patterns)

package pipeline

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

var (
	// options.redacted.services.<NAME> = { ... }
	// Captures the service name. Anchored to `options.` to avoid matching
	// usage-side `redacted.services.X.Y = ...` (handled by separate patterns).
	reNixServiceDecl = regexp.MustCompile(
		`(?m)^\s*options\.redacted\.services\.([a-zA-Z_][a-zA-Z0-9_-]*)\s*=`,
	)

	// baf.pub_topic = mkOption { ... default = "<topic>"; ... }
	// `[^}]*` means we can't match nested braces; fine for these modules
	// (mkOption blocks don't nest braces in PSM's style).
	reNixBafPubTopic = regexp.MustCompile(
		`(?s)baf\.pub_topic\s*=\s*mkOption\s*\{[^}]*?default\s*=\s*"([^"]+)"`,
	)

	// baf.sub_topics = mkOption { ... default = [ "a" "b" "c" ]; ... }
	// Captures the list contents between [ and ].
	reNixBafSubTopicsList = regexp.MustCompile(
		`(?s)baf\.sub_topics\s*=\s*mkOption\s*\{[^}]*?default\s*=\s*\[([^\]]*)\]`,
	)

	// redacted.services.<NAME>.additional_sub_topics = [ "X" "Y" ];
	// Captures (service_name, list_contents).
	reNixAdditionalSubTopics = regexp.MustCompile(
		`(?s)redacted\.services\.([a-zA-Z_][a-zA-Z0-9_-]*)\.additional_sub_topics\s*=\s*\[([^\]]*)\]`,
	)

	// A string literal inside a Nix list — used to split `[ "a" "b" "c" ]`.
	reNixStringLiteral = regexp.MustCompile(`"([^"]+)"`)

	// pubmsg <topic> — imperative publish in a systemd script.
	// Topic is a non-interpolation literal (skip ${...} templated cases
	// since those are captured by the declarative pass).
	reNixPubmsgLiteral = regexp.MustCompile(
		`\bpubmsg\s+([a-zA-Z0-9_][a-zA-Z0-9_-]*)\b`,
	)

	// submsg <topic1> <topic2> ... | — imperative subscribe in a script.
	// Captures the whole argument chunk up to the first `|` pipe or `\\`
	// line-continuation. Topics then extracted via reNixStringLiteral +
	// bare-identifier fallback.
	reNixSubmsgLine = regexp.MustCompile(
		`\bsubmsg\s+([^\n|\\]+)`,
	)

	// Bare identifier — used to split submsg argument chunks into topics.
	reNixBareIdent = regexp.MustCompile(
		`\b([a-zA-Z_][a-zA-Z0-9_-]*)\b`,
	)
)

// nixServiceInfo is one parsed Nix service module.
type nixServiceInfo struct {
	serviceName string   // from options.redacted.services.<name>
	pubTopic    string   // "" if not declared
	subTopics   []string // base list, excluding conditional appends
	// Imperative references from systemd scripts
	impPubTopics []string
	impSubTopics []string
	// additional_sub_topics keyed by target service name (often references a
	// different service, e.g., nazgul-radar-services.nix adds "simd" to trackerd).
	additionalSubsByService map[string][]string
	declaredIn              string
}

// passNixServices walks .nix files under RepoPath, parses redacted service
// declarations, and emits Service + Topic nodes with PUBLISHES_TO/
// SUBSCRIBES_TO edges. No-op silently on repos with no matching patterns.
func (p *Pipeline) passNixServices() {
	var nixFiles []string
	err := filepath.WalkDir(p.RepoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip inaccessible entries
		}
		if d.IsDir() {
			if discover.IGNORE_PATTERNS[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".nix") {
			nixFiles = append(nixFiles, path)
		}
		return nil
	})
	if err != nil {
		slog.Warn("pass.nix_services.walk", "err", err)
		return
	}
	if len(nixFiles) == 0 {
		return
	}

	serviceCount := 0
	topicCount := 0
	edgeCount := 0

	for _, absPath := range nixFiles {
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		source := string(data)

		info := parseNixServiceFile(source)
		if info.serviceName == "" && len(info.additionalSubsByService) == 0 {
			continue
		}

		relPath, err := filepath.Rel(p.RepoPath, absPath)
		if err != nil {
			relPath = absPath
		}
		relPath = filepath.ToSlash(relPath)
		info.declaredIn = relPath

		// Emit Service node (if this file declares one).
		var serviceID int64
		if info.serviceName != "" {
			svcNode := p.nixServiceNode(info.serviceName, relPath)
			id, err := p.Store.UpsertNode(svcNode)
			if err == nil && id > 0 {
				serviceID = id
				serviceCount++
			}
		}

		// Emit PUBLISHES_TO edges
		if serviceID > 0 {
			for _, topic := range uniqueStrings(append([]string{info.pubTopic}, info.impPubTopics...)) {
				if topic == "" {
					continue
				}
				topicID, ok := p.upsertNixTopic(topic, relPath)
				if !ok {
					continue
				}
				topicCount++
				if p.insertNixEdge(serviceID, topicID, "PUBLISHES_TO", topic == info.pubTopic, relPath) {
					edgeCount++
				}
			}
			for _, topic := range uniqueStrings(append(append([]string{}, info.subTopics...), info.impSubTopics...)) {
				if topic == "" {
					continue
				}
				topicID, ok := p.upsertNixTopic(topic, relPath)
				if !ok {
					continue
				}
				topicCount++
				declarative := false
				for _, d := range info.subTopics {
					if d == topic {
						declarative = true
						break
					}
				}
				if p.insertNixEdge(serviceID, topicID, "SUBSCRIBES_TO", declarative, relPath) {
					edgeCount++
				}
			}
		}

		// additional_sub_topics: cross-file subscriptions on OTHER services.
		for targetSvc, topics := range info.additionalSubsByService {
			targetID := p.findOrCreateServiceID(targetSvc, relPath)
			if targetID == 0 {
				continue
			}
			for _, topic := range uniqueStrings(topics) {
				topicID, ok := p.upsertNixTopic(topic, relPath)
				if !ok {
					continue
				}
				topicCount++
				if p.insertNixEdge(targetID, topicID, "SUBSCRIBES_TO", true, relPath) {
					edgeCount++
				}
			}
		}
	}

	if serviceCount > 0 || edgeCount > 0 {
		slog.Info("pass.nix_services",
			"nix_files", len(nixFiles),
			"services", serviceCount,
			"topics", topicCount,
			"edges", edgeCount,
		)
	}
}

// parseNixServiceFile extracts service + topic bindings from one Nix file.
// Pure function — safe to unit-test with raw source strings.
func parseNixServiceFile(source string) nixServiceInfo {
	info := nixServiceInfo{
		additionalSubsByService: make(map[string][]string),
	}

	// Service name (first declaration wins; most files declare one).
	if m := reNixServiceDecl.FindStringSubmatch(source); len(m) == 2 {
		info.serviceName = m[1]
	}

	// Declarative pub topic
	if m := reNixBafPubTopic.FindStringSubmatch(source); len(m) == 2 {
		info.pubTopic = m[1]
	}

	// Declarative sub topics
	if m := reNixBafSubTopicsList.FindStringSubmatch(source); len(m) == 2 {
		info.subTopics = extractNixStringList(m[1])
	}

	// additional_sub_topics (often on OTHER services in common config files)
	for _, m := range reNixAdditionalSubTopics.FindAllStringSubmatch(source, -1) {
		targetSvc := m[1]
		topics := extractNixStringList(m[2])
		info.additionalSubsByService[targetSvc] = append(
			info.additionalSubsByService[targetSvc], topics...,
		)
	}

	// Imperative pubmsg / submsg references in script blocks
	for _, m := range reNixPubmsgLiteral.FindAllStringSubmatch(source, -1) {
		info.impPubTopics = append(info.impPubTopics, m[1])
	}
	for _, m := range reNixSubmsgLine.FindAllStringSubmatch(source, -1) {
		info.impSubTopics = append(info.impSubTopics, extractSubmsgTopics(m[1])...)
	}

	return info
}

// extractNixStringList pulls all "quoted" items from a Nix list body.
// Conditional `++ (if ...)` appends are intentionally NOT resolved.
func extractNixStringList(body string) []string {
	matches := reNixStringLiteral.FindAllStringSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if m[1] != "" {
			out = append(out, m[1])
		}
	}
	return out
}

// extractSubmsgTopics parses a submsg argument chunk. Topics may be quoted
// literals OR bare identifiers. Skip nix-interpolated ${...} chunks.
func extractSubmsgTopics(chunk string) []string {
	// Strip ${...} interpolations so we don't grab "cfg", "baf", etc.
	stripped := stripNixInterpolations(chunk)
	var out []string
	// Quoted literals first
	for _, m := range reNixStringLiteral.FindAllStringSubmatch(stripped, -1) {
		if m[1] != "" {
			out = append(out, m[1])
		}
	}
	// Bare identifiers — only if no quoted topics were found
	if len(out) == 0 {
		for _, m := range reNixBareIdent.FindAllStringSubmatch(stripped, -1) {
			tok := m[1]
			// Skip likely non-topic tokens (nix/shell keywords)
			if isNixKeyword(tok) {
				continue
			}
			out = append(out, tok)
		}
	}
	return out
}

// stripNixInterpolations removes `${...}` chunks from a string. Simple
// depth-tracking since nix interpolations can nest.
func stripNixInterpolations(s string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		if depth == 0 && i+1 < len(s) && s[i] == '$' && s[i+1] == '{' {
			depth = 1
			i++ // skip the {
			continue
		}
		if depth > 0 {
			if s[i] == '{' {
				depth++
			} else if s[i] == '}' {
				depth--
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// isNixKeyword returns true for tokens that shouldn't be treated as topics.
func isNixKeyword(tok string) bool {
	switch tok {
	case "if", "then", "else", "let", "in", "with", "true", "false", "null",
		"builtins", "toString", "concatStringsSep", "pkgs", "redacted",
		"cfg", "baf", "bin":
		return true
	}
	return false
}

// uniqueStrings returns the input slice with duplicates removed, preserving order.
func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// nixServiceNode builds a Service node for the given service name.
func (p *Pipeline) nixServiceNode(name, declaredIn string) *store.Node {
	qn := p.ProjectName + ".__service__." + name
	return &store.Node{
		Project:       p.ProjectName,
		Label:         "Service",
		Name:          name,
		QualifiedName: qn,
		FilePath:      declaredIn,
		Properties: map[string]any{
			"source":           "nix",
			"declared_in_file": declaredIn,
		},
	}
}

// findOrCreateServiceID returns a Service node id, creating a stub if the
// service isn't already in the graph. Used by additional_sub_topics which
// references services defined in separate files.
func (p *Pipeline) findOrCreateServiceID(name, declaredIn string) int64 {
	qn := p.ProjectName + ".__service__." + name
	if node, err := p.Store.FindNodeByQN(p.ProjectName, qn); err == nil && node != nil {
		return node.ID
	}
	id, err := p.Store.UpsertNode(p.nixServiceNode(name, declaredIn))
	if err != nil {
		return 0
	}
	return id
}

// upsertNixTopic emits a Topic node for the given topic string. Unified
// label/QN scheme with zenoh.go's zenohTopicNode.
func (p *Pipeline) upsertNixTopic(topic, declaredIn string) (int64, bool) {
	sanitized := sanitizeTopicForQN(topic)
	qn := p.ProjectName + ".__topic__." + sanitized
	node := &store.Node{
		Project:       p.ProjectName,
		Label:         "Topic",
		Name:          topic,
		QualifiedName: qn,
		FilePath:      declaredIn,
		Properties: map[string]any{
			"scope":            "local",
			"shared":           isSharedTopic(topic),
			"declared_in_file": declaredIn,
			"source":           "nix",
		},
	}
	id, err := p.Store.UpsertNode(node)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// insertNixEdge creates one edge. `declarative=true` means the topic came
// from an mkOption default (canonical); `false` means imperative script
// reference (may duplicate the declarative form but ON CONFLICT merges).
func (p *Pipeline) insertNixEdge(srcID, tgtID int64, edgeType string, declarative bool, declaredIn string) bool {
	tier := store.ConfidenceInferred
	if declarative {
		tier = store.ConfidenceExtracted
	}
	edge := &store.Edge{
		Project:  p.ProjectName,
		SourceID: srcID,
		TargetID: tgtID,
		Type:     edgeType,
		Properties: map[string]any{
			"source":           "nix",
			"declarative":      declarative,
			"confidence_tier":  tier,
			"declared_in_file": declaredIn,
		},
	}
	_, err := p.Store.InsertEdge(edge)
	return err == nil
}
