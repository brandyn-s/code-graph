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

	// baf.pub_topic[_<suffix>] = mkOption { ... default = "<topic>"; ... }
	// Matches the canonical baf.pub_topic AND named variants like
	// baf.pub_topic_fast (anavd has both — separate topics for different
	// rate classes). `[^}]*` means we can't match nested braces; fine for
	// these modules (mkOption blocks don't nest braces in PSM's style).
	reNixBafPubTopic = regexp.MustCompile(
		`(?s)baf\.pub_topic(?:_[a-zA-Z_][a-zA-Z0-9_]*)?\s*=\s*mkOption\s*\{[^}]*?default\s*=\s*"([^"]+)"`,
	)

	// baf.<name>_sub_topic = mkOption { default = "<topic>"; }
	// SINGULAR scalar variant — used by adsbd (baf.ahrs_sub_topic = "sbfd").
	// Distinct from baf.sub_topics (plural list). Currently only one PSM
	// service uses this pattern, but it's a legitimate option type.
	reNixBafSubTopicSingular = regexp.MustCompile(
		`(?s)baf\.([a-zA-Z_][a-zA-Z0-9_]*)_sub_topic\s*=\s*mkOption\s*\{[^}]*?default\s*=\s*"([^"]+)"`,
	)

	// baf.sub_topics = mkOption { ... default = [ "a" "b" "c" ]; ... }
	// Captures the FIRST `[ ... ]` — used as the base literal list, which
	// downstream code marks as EXTRACTED (confident) confidence tier.
	reNixBafSubTopicsList = regexp.MustCompile(
		`(?s)baf\.sub_topics\s*=\s*mkOption\s*\{[^}]*?default\s*=\s*\[([^\]]*)\]`,
	)

	// baf.sub_topics whole default expression, including conditional appends
	// and lib.optional(s) patterns:
	//   default = [ "a" "b" ]
	//     ++ (if c then [ "d" ] else [ "e" ])
	//     ++ lib.optionals foo [ "f" "g" ];
	// Captures from `default = ` up to the terminating `;` — Nix doesn't
	// use `;` inside string literals at this level, so `[^;]+` is safe.
	// Used to surface conditional topics (AMBIGUOUS tier, since only ONE
	// branch of if/else is taken at runtime).
	reNixBafSubTopicsFullExpr = regexp.MustCompile(
		`(?s)baf\.sub_topics\s*=\s*mkOption\s*\{[^}]*?default\s*=\s*([^;]+);`,
	)

	// redacted.services.<NAME>.additional_sub_topics = [ "X" "Y" ];
	// Captures (service_name, list_contents).
	reNixAdditionalSubTopics = regexp.MustCompile(
		`(?s)redacted\.services\.([a-zA-Z_][a-zA-Z0-9_-]*)\.additional_sub_topics\s*=\s*\[([^\]]*)\]`,
	)

	// A string literal inside a Nix list — used to split `[ "a" "b" "c" ]`.
	reNixStringLiteral = regexp.MustCompile(`"([^"]+)"`)

	// /bin/pubmsg <topic> — imperative publish in a systemd script.
	// Requires `/bin/` prefix to anchor command-invocation context. Without
	// this, comments ("# start pubmsg for ..."), Nix package lists
	// ([ submsg pubmsg cfg ]), and multi-line declarations produce large
	// numbers of false positives (measured on PSM: ~20 spurious edges).
	// Topic is a non-interpolation literal; skip ${...} templated cases
	// (captured by the declarative pass).
	reNixPubmsgLiteral = regexp.MustCompile(
		`/bin/pubmsg[ \t]+([a-zA-Z0-9_][a-zA-Z0-9_-]*)\b`,
	)

	// /bin/submsg <topic1> <topic2> ... | — imperative subscribe in a script.
	// Same `/bin/` prefix requirement as pubmsg. Captures everything from
	// submsg to first pipe/newline/line-continuation, then extractSubmsgTopics
	// parses topics out of that chunk.
	reNixSubmsgLine = regexp.MustCompile(
		`/bin/submsg[ \t]+([^\n|\\]+)`,
	)

	// Bare identifier — used to split submsg argument chunks into topics.
	reNixBareIdent = regexp.MustCompile(
		`\b([a-zA-Z_][a-zA-Z0-9_-]*)\b`,
	)

	// `${pkgs.redacted.<package>}/bin/<binary>` — the redacted-Nix idiom for
	// invoking a Rust binary from a systemd script. Captures (package, binary).
	// Used to emit `Service ── RUNS_BINARY ──> Module` edges, which close
	// the service-to-implementation loop ("show me the code for canstatd").
	//
	// Filters out pubmsg/submsg here — those are framework helpers, not the
	// service implementation itself.
	reNixredactedBinary = regexp.MustCompile(
		`\$\{pkgs\.redacted\.([a-zA-Z0-9_-]+)\}/bin/([a-zA-Z_][a-zA-Z0-9_-]*)\b`,
	)

	// Cargo.toml [package] name = "X". Standalone package manifests only
	// (workspace-only Cargo.toml has no [package] section, returns no match).
	reCargoPackageName = regexp.MustCompile(
		`(?m)^\s*name\s*=\s*"([^"]+)"`,
	)

	// Cargo.toml [[bin]] name = "X". Multiple [[bin]] sections per crate
	// possible; captures all explicit binary declarations. If no [[bin]],
	// the binary defaults to the [package].name.
	reCargoBinName = regexp.MustCompile(
		`(?ms)^\s*\[\[bin\]\][^\[]*?^\s*name\s*=\s*"([^"]+)"`,
	)
)

// nixServiceInfo is one parsed Nix service module.
type nixServiceInfo struct {
	serviceName string // from options.redacted.services.<name>
	pubTopic    string // primary baf.pub_topic default; "" if not declared
	// pubTopicVariants captures additional `baf.pub_topic_<suffix>` defaults
	// (e.g., anavd's `baf.pub_topic_fast = "anavd-fast"`). Each variant is
	// emitted as a separate Service → Topic edge.
	pubTopicVariants []string
	subTopics        []string // base literal list only (unconditional)
	// conditionalSubTopics: additional topics from `++ (if ...)` or
	// `++ lib.optional(s)` tails. Only ONE branch of each if/else is
	// taken at runtime, but all branches are captured as a union since
	// the graph can't know which. Marked AMBIGUOUS downstream.
	conditionalSubTopics []string
	// Imperative references from systemd scripts
	impPubTopics []string
	impSubTopics []string
	// additional_sub_topics keyed by target service name (often references a
	// different service, e.g., nazgul-radar-services.nix adds "simd" to trackerd).
	additionalSubsByService map[string][]string
	// runsBinaries: package names referenced via `${pkgs.redacted.<pkg>}/bin/<binary>`.
	// Filtered to exclude pubmsg/submsg (framework helpers, not service code).
	// Used to emit Service ── RUNS_BINARY ──> Module edges.
	runsBinaries []string
	declaredIn   string
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
	runsBinaryCount := 0

	// Build Rust binary index once for the repo. Empty if no Cargo.toml
	// files found (non-Rust repo); RUNS_BINARY emission no-ops cleanly.
	rustBinaryMap := p.buildRustBinaryMap()

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
		// Re-resolve ID via FindNodeByQN — UpsertNode's LastInsertId is
		// unreliable on ON CONFLICT DO UPDATE paths (store.UpsertNode doc).
		var serviceID int64
		if info.serviceName != "" {
			svcNode := p.nixServiceNode(info.serviceName, relPath)
			if _, err := p.Store.UpsertNode(svcNode); err == nil {
				if resolved, err2 := p.Store.FindNodeByQN(p.ProjectName, svcNode.QualifiedName); err2 == nil && resolved != nil {
					serviceID = resolved.ID
					serviceCount++
				}
			}
		}

		// Emit PUBLISHES_TO edges. Combines: primary baf.pub_topic + named
		// pub_topic_<suffix> variants + imperative pubmsg literals.
		if serviceID > 0 {
			declarativePubs := make(map[string]struct{})
			if info.pubTopic != "" {
				declarativePubs[info.pubTopic] = struct{}{}
			}
			for _, t := range info.pubTopicVariants {
				declarativePubs[t] = struct{}{}
			}
			allPubs := []string{info.pubTopic}
			allPubs = append(allPubs, info.pubTopicVariants...)
			allPubs = append(allPubs, info.impPubTopics...)
			for _, topic := range uniqueStrings(allPubs) {
				if topic == "" {
					continue
				}
				topicID, ok := p.upsertNixTopic(topic, relPath)
				if !ok {
					continue
				}
				topicCount++
				_, isDeclarative := declarativePubs[topic]
				if p.insertNixEdge(serviceID, topicID, "PUBLISHES_TO", isDeclarative, relPath) {
					edgeCount++
				}
			}
			// Unconditional subs: base literal list + imperative script refs.
			// EXTRACTED confidence since always subscribed.
			baseSubs := uniqueStrings(append(append([]string{}, info.subTopics...), info.impSubTopics...))
			baseSubSet := make(map[string]struct{}, len(baseSubs))
			for _, t := range baseSubs {
				baseSubSet[t] = struct{}{}
			}
			for _, topic := range baseSubs {
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
			// Conditional subs from `++ (if ...)` tails. Only one branch is
			// taken at runtime — emit as AMBIGUOUS tier, skip topics already
			// covered by the unconditional set.
			for _, topic := range uniqueStrings(info.conditionalSubTopics) {
				if topic == "" {
					continue
				}
				if _, seen := baseSubSet[topic]; seen {
					continue
				}
				topicID, ok := p.upsertNixTopic(topic, relPath)
				if !ok {
					continue
				}
				topicCount++
				if p.insertNixConditionalEdge(serviceID, topicID, "SUBSCRIBES_TO", relPath) {
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

		// RUNS_BINARY edges: Service → Module (the Rust crate's main entry).
		// Only emit when the Service node was successfully created AND the
		// referenced binary maps to a known crate AND that crate's Module
		// node exists in the graph.
		if serviceID > 0 {
			for _, bin := range info.runsBinaries {
				entry, ok := rustBinaryMap[bin]
				if !ok {
					continue
				}
				moduleQN := p.ProjectName
				if entry.cratePath != "" {
					moduleQN += "." + entry.cratePath
				}
				moduleQN += ".src.main"
				moduleNode, err := p.Store.FindNodeByQN(p.ProjectName, moduleQN)
				if err != nil || moduleNode == nil {
					// Try lib.rs fallback for crates without a main.rs binary.
					libQN := strings.TrimSuffix(moduleQN, ".main") + ".lib"
					moduleNode, err = p.Store.FindNodeByQN(p.ProjectName, libQN)
					if err != nil || moduleNode == nil {
						continue
					}
				}
				if p.insertNixRunsBinaryEdge(serviceID, moduleNode.ID, bin, entry, relPath) {
					runsBinaryCount++
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
			"runs_binary_edges", runsBinaryCount,
		)
	}
}

// insertNixRunsBinaryEdge creates a Service ── RUNS_BINARY ──> Module edge
// linking a systemd service to the Rust crate's source module.
func (p *Pipeline) insertNixRunsBinaryEdge(srcID, tgtID int64, binaryName string, entry rustBinaryEntry, declaredIn string) bool {
	edge := &store.Edge{
		Project:  p.ProjectName,
		SourceID: srcID,
		TargetID: tgtID,
		Type:     "RUNS_BINARY",
		Properties: map[string]any{
			"source":           "nix",
			"binary_name":      binaryName,
			"crate_name":       entry.crateName,
			"crate_path":       entry.cratePath,
			"confidence_tier":  store.ConfidenceExtracted,
			"declared_in_file": declaredIn,
		},
	}
	_, err := p.Store.InsertEdge(edge)
	return err == nil
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

	// Declarative pub topic(s) — supports `baf.pub_topic` plus named
	// variants like `baf.pub_topic_fast`. First match becomes the primary
	// pub; additional matches are stored as variants.
	for i, m := range reNixBafPubTopic.FindAllStringSubmatch(source, -1) {
		if i == 0 {
			info.pubTopic = m[1]
		} else {
			info.pubTopicVariants = append(info.pubTopicVariants, m[1])
		}
	}

	// Declarative singular sub topic — `baf.<name>_sub_topic = mkOption { default = "X" }`.
	// adsbd uses `baf.ahrs_sub_topic = "sbfd"` (only PSM example).
	for _, m := range reNixBafSubTopicSingular.FindAllStringSubmatch(source, -1) {
		info.subTopics = append(info.subTopics, m[2])
	}

	// Declarative sub topics — base literal list (always subscribed).
	if m := reNixBafSubTopicsList.FindStringSubmatch(source); len(m) == 2 {
		info.subTopics = extractNixStringList(m[1])
	}

	// Conditional sub topics — topics from `++ (if ...)` or `++ lib.optional(s)`
	// tails. Computed as the full-expression topic set MINUS the base list.
	if m := reNixBafSubTopicsFullExpr.FindStringSubmatch(source); len(m) == 2 {
		allTopics := extractNixStringList(m[1])
		baseSet := make(map[string]struct{}, len(info.subTopics))
		for _, t := range info.subTopics {
			baseSet[t] = struct{}{}
		}
		seen := make(map[string]struct{})
		for _, t := range allTopics {
			if _, inBase := baseSet[t]; inBase {
				continue
			}
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			info.conditionalSubTopics = append(info.conditionalSubTopics, t)
		}
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

	// `${pkgs.redacted.X}/bin/Y` references — Y is the binary the service runs.
	// Filter out pubmsg/submsg (framework helpers, not the service code itself).
	seen := make(map[string]struct{})
	for _, m := range reNixredactedBinary.FindAllStringSubmatch(source, -1) {
		bin := m[2]
		if bin == "pubmsg" || bin == "submsg" {
			continue
		}
		if _, dup := seen[bin]; dup {
			continue
		}
		seen[bin] = struct{}{}
		info.runsBinaries = append(info.runsBinaries, bin)
	}

	return info
}

// rustBinaryEntry maps a Rust binary name to its source crate.
type rustBinaryEntry struct {
	cratePath string // dotted relative path, e.g., "canstatd" or "calibration.calibd"
	crateName string // [package] name field
}

// buildRustBinaryMap scans Cargo.toml files under RepoPath and produces
// a map from binary name to crate. Binary name defaults to the package
// name; if `[[bin]] name = "X"` is present, X is used.
//
// Used by RUNS_BINARY edge emission to resolve `${pkgs.redacted.X}/bin/Y`
// references in Nix scripts back to the Rust crate's Module node.
func (p *Pipeline) buildRustBinaryMap() map[string]rustBinaryEntry {
	out := make(map[string]rustBinaryEntry)
	_ = filepath.WalkDir(p.RepoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if d.IsDir() {
			if d.Name() == "target" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "Cargo.toml" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		source := string(data)

		// [package] name — first match wins. Workspace-only manifests have
		// no [package] section, return early.
		pkgName := ""
		if m := reCargoPackageName.FindStringSubmatch(source); len(m) == 2 {
			pkgName = m[1]
		}
		if pkgName == "" {
			return nil
		}

		crateDir := filepath.Dir(path)
		relCrate, err := filepath.Rel(p.RepoPath, crateDir)
		if err != nil {
			return nil
		}
		dotted := strings.ReplaceAll(filepath.ToSlash(relCrate), "/", ".")
		// Repo root crate has empty rel path; treat as "" so QN is just package.
		if dotted == "." {
			dotted = ""
		}
		entry := rustBinaryEntry{cratePath: dotted, crateName: pkgName}

		// Default binary = package name.
		out[pkgName] = entry
		// Replace `-` with `_` (Cargo's default binary lookup convention)
		// AND keep the original; Nix references can use either form.
		if strings.Contains(pkgName, "-") {
			out[strings.ReplaceAll(pkgName, "-", "_")] = entry
		}

		// Explicit [[bin]] name fields override / supplement.
		for _, m := range reCargoBinName.FindAllStringSubmatch(source, -1) {
			out[m[1]] = entry
		}
		return nil
	})
	return out
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
//
// Always re-resolves via FindNodeByQN after UpsertNode to avoid stale
// LastInsertId from ON CONFLICT DO UPDATE paths.
func (p *Pipeline) findOrCreateServiceID(name, declaredIn string) int64 {
	qn := p.ProjectName + ".__service__." + name
	if node, err := p.Store.FindNodeByQN(p.ProjectName, qn); err == nil && node != nil {
		return node.ID
	}
	if _, err := p.Store.UpsertNode(p.nixServiceNode(name, declaredIn)); err != nil {
		return 0
	}
	resolved, err := p.Store.FindNodeByQN(p.ProjectName, qn)
	if err != nil || resolved == nil {
		return 0
	}
	return resolved.ID
}

// upsertNixTopic emits a Topic node for the given topic string. Unified
// label/QN scheme with zenoh.go's zenohTopicNode.
//
// WHY the FindNodeByQN re-read: store.UpsertNode's LastInsertId can return
// a stale ID on ON CONFLICT DO UPDATE paths (documented in store.UpsertNode
// doc comment). Trusting it silently breaks downstream edge inserts with
// FK-constraint failures. Always resolve by QN to get the canonical ID.
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
	if _, err := p.Store.UpsertNode(node); err != nil {
		return 0, false
	}
	resolved, err := p.Store.FindNodeByQN(p.ProjectName, qn)
	if err != nil || resolved == nil {
		return 0, false
	}
	return resolved.ID, true
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

// insertNixConditionalEdge emits a topic edge for a conditional append
// (`++ (if ...)` / `++ lib.optional(s)`). Marked AMBIGUOUS because only
// one branch executes at runtime; the graph captures all branches as a
// union. Callers (e.g., documentation pipelines) should filter on
// `r.conditional = false` when they need "always active" relationships.
func (p *Pipeline) insertNixConditionalEdge(srcID, tgtID int64, edgeType, declaredIn string) bool {
	edge := &store.Edge{
		Project:  p.ProjectName,
		SourceID: srcID,
		TargetID: tgtID,
		Type:     edgeType,
		Properties: map[string]any{
			"source":           "nix",
			"declarative":      true,
			"conditional":      true,
			"confidence_tier":  store.ConfidenceAmbiguous,
			"declared_in_file": declaredIn,
		},
	}
	_, err := p.Store.InsertEdge(edge)
	return err == nil
}
