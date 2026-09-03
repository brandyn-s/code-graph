package pipeline

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/store"
)

// configExtensions are file extensions considered "config files".
var configExtensions = map[string]bool{
	".env": true, ".toml": true, ".ini": true, ".yaml": true, ".yml": true,
	".cfg": true, ".properties": true, ".json": true, ".xml": true, ".conf": true,
}

// manifestFiles are package manifest filenames used for dependency→import linking.
var manifestFiles = map[string]bool{
	"Cargo.toml": true, "package.json": true, "go.mod": true,
	"requirements.txt": true, "Gemfile": true, "build.gradle": true,
	"pom.xml": true, "composer.json": true,
}

// depSectionNames are section/key names that indicate dependency lists.
var depSectionNames = map[string]bool{
	"dependencies": true, "devDependencies": true, "peerDependencies": true,
	"dev-dependencies": true, "build-dependencies": true,
}

// configFileRefRe matches string literals referencing config files.
var configFileRefRe = regexp.MustCompile(
	`["']([^"']*\.(toml|yaml|yml|ini|json|xml|conf|cfg|env))["']`)

// passConfigLinker runs 3 post-flush strategies to link config↔code.
func (p *Pipeline) passConfigLinker() {
	t := time.Now()
	keyEdges := p.matchConfigKeySymbols()
	slog.Info("configlinker.strategy", "name", "key_symbol", "edges", len(keyEdges))

	t2 := time.Now()
	depEdges := p.matchDependencyImports()
	slog.Info("configlinker.strategy", "name", "dep_import", "edges", len(depEdges), "elapsed", time.Since(t2))

	t3 := time.Now()
	refEdges := p.matchConfigFileRefs()
	slog.Info("configlinker.strategy", "name", "file_ref", "edges", len(refEdges), "elapsed", time.Since(t3))

	t4 := time.Now()
	tfEnvEdges := p.matchTerraformEnvVars()
	slog.Info("configlinker.strategy", "name", "terraform_env", "edges", len(tfEnvEdges), "elapsed", time.Since(t4))

	all := make([]*store.Edge, 0, len(keyEdges)+len(depEdges)+len(refEdges)+len(tfEnvEdges))
	all = append(all, keyEdges...)
	all = append(all, depEdges...)
	all = append(all, refEdges...)
	all = append(all, tfEnvEdges...)

	if len(all) > 0 {
		if err := p.Store.InsertEdgeBatch(all); err != nil {
			slog.Warn("configlinker.write_err", "err", err)
		}
	}

	slog.Info("configlinker.done",
		"key_symbol", len(keyEdges),
		"dep_import", len(depEdges),
		"file_ref", len(refEdges),
		"terraform_env", len(tfEnvEdges),
		"total", len(all),
		"elapsed", time.Since(t))
}

// --- Strategy 1: Config Key → Code Symbol ---

// normalizeConfigKey splits a config key on camelCase, underscores, dots, hyphens,
// lowercases all tokens, and joins with underscore.
func normalizeConfigKey(key string) (normalized string, tokens []string) {
	// Split on non-alphanumeric chars first
	parts := strings.FieldsFunc(key, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	})

	for _, part := range parts {
		camel := splitCamelCase(part)
		for _, w := range camel {
			tokens = append(tokens, strings.ToLower(w))
		}
	}

	normalized = strings.Join(tokens, "_")
	return
}

// configEntry pairs a config node with its normalized key.
type configEntry struct {
	node       *store.Node
	normalized string
}

// collectConfigEntries returns config Variable nodes with min 2 tokens, each ≥3 chars.
func collectConfigEntries(vars []*store.Node) []configEntry {
	var entries []configEntry
	for _, v := range vars {
		if !hasConfigExtension(v.FilePath) {
			continue
		}
		norm, tokens := normalizeConfigKey(v.Name)
		if len(tokens) < 2 {
			continue
		}
		allLong := true
		for _, t := range tokens {
			if len(t) < 3 {
				allLong = false
				break
			}
		}
		if allLong {
			entries = append(entries, configEntry{node: v, normalized: norm})
		}
	}
	return entries
}

// collectCodeNodes returns Function/Variable/Class nodes not from config files.
func (p *Pipeline) collectCodeNodes() []*store.Node {
	var codeNodes []*store.Node
	for _, label := range []string{"Function", "Variable", "Class"} {
		nodes, err := p.Store.FindNodesByLabel(p.ProjectName, label)
		if err != nil {
			continue
		}
		for _, n := range nodes {
			if !hasConfigExtension(n.FilePath) {
				codeNodes = append(codeNodes, n)
			}
		}
	}
	return codeNodes
}

// codeNodeWithNorm caches the normalized name alongside the node so the
// key-symbol matcher does not re-normalize every code node for every
// config entry. (Plan 5 Phase D: P99 phase tail fix — see PHASE_D_PERF.md.)
type codeNodeWithNorm struct {
	node *store.Node
	norm string
}

// matchConfigKeySymbols links config Variable nodes to code symbols when
// the normalized config key is a contiguous substring of the normalized code name.
//
// Plan 5 Phase D: pre-normalize all code-node names once before the
// O(|entries|×|codeNodes|) match loop. Previously, normalizeConfigKey was
// called inside the inner loop, doing |entries|×|codeNodes| normalizations
// when |codeNodes| is sufficient. On the code-graph self-index this
// dropped the configlinker phase from 53.18s to under 10s.
func (p *Pipeline) matchConfigKeySymbols() []*store.Edge {
	configVars, err := p.Store.FindNodesByLabel(p.ProjectName, "Variable")
	if err != nil {
		return nil
	}

	entries := collectConfigEntries(configVars)
	if len(entries) == 0 {
		return nil
	}

	codeNodes := p.collectCodeNodes()

	// Pre-normalize each code node's name exactly once.
	cached := make([]codeNodeWithNorm, 0, len(codeNodes))
	for _, code := range codeNodes {
		norm, _ := normalizeConfigKey(code.Name)
		if norm == "" {
			continue
		}
		cached = append(cached, codeNodeWithNorm{node: code, norm: norm})
	}

	var edges []*store.Edge
	for _, ce := range entries {
		for _, c := range cached {
			var confidence float64
			switch {
			case c.norm == ce.normalized:
				confidence = 0.85 // exact match
			case strings.Contains(c.norm, ce.normalized):
				confidence = 0.75 // substring match
			default:
				continue
			}

			edges = append(edges, &store.Edge{
				Project:  p.ProjectName,
				SourceID: c.node.ID,
				TargetID: ce.node.ID,
				Type:     "CONFIGURES",
				Properties: map[string]any{
					"strategy":        "key_symbol",
					"confidence_tier": store.ConfidenceAmbiguous,
					"confidence":      confidence,
					"config_key":      ce.node.Name,
				},
			})
		}
	}
	return edges
}

// --- Strategy 2: Dependency Name → Import Match ---

// depEntry pairs a manifest dependency node with its name.
type depEntry struct {
	node *store.Node
	name string
}

// collectManifestDeps returns dependency Variable nodes from package manifest files.
func collectManifestDeps(vars []*store.Node) []depEntry {
	var deps []depEntry
	for _, v := range vars {
		basename := filepath.Base(v.FilePath)
		if !manifestFiles[basename] {
			continue
		}
		isDep := false
		qnLower := strings.ToLower(v.QualifiedName)
		for sec := range depSectionNames {
			if strings.Contains(qnLower, strings.ToLower(sec)) {
				isDep = true
				break
			}
		}
		if !isDep && basename == "Cargo.toml" {
			isDep = isDependencyChild(v)
		}
		if isDep {
			deps = append(deps, depEntry{node: v, name: v.Name})
		}
	}
	return deps
}

// resolveEdgeNodes builds lookup maps for source and target nodes of edges.
func (p *Pipeline) resolveEdgeNodes(edges []*store.Edge) (source, target map[int64]*store.Node) {
	ids := make(map[int64]struct{})
	for _, e := range edges {
		ids[e.SourceID] = struct{}{}
		ids[e.TargetID] = struct{}{}
	}
	lookup := make(map[int64]*store.Node, len(ids))
	for id := range ids {
		n, err := p.Store.FindNodeByID(id)
		if err == nil && n != nil {
			lookup[id] = n
		}
	}
	return lookup, lookup
}

// matchDependencyImports links dependency entries in package manifests
// to code modules that import them.
func (p *Pipeline) matchDependencyImports() []*store.Edge {
	configVars, err := p.Store.FindNodesByLabel(p.ProjectName, "Variable")
	if err != nil {
		return nil
	}

	deps := collectManifestDeps(configVars)
	if len(deps) == 0 {
		return nil
	}

	importEdges, err := p.Store.FindEdgesByType(p.ProjectName, "IMPORTS")
	if err != nil {
		return nil
	}

	nodeLookup, _ := p.resolveEdgeNodes(importEdges)

	var edges []*store.Edge
	for _, dep := range deps {
		depNameLower := strings.ToLower(dep.name)
		for _, impEdge := range importEdges {
			target := nodeLookup[impEdge.TargetID]
			source := nodeLookup[impEdge.SourceID]
			if target == nil || source == nil {
				continue
			}

			targetNameLower := strings.ToLower(target.Name)
			targetQNLower := strings.ToLower(target.QualifiedName)

			var confidence float64
			switch {
			case targetNameLower == depNameLower:
				confidence = 0.95
			case strings.Contains(targetQNLower, depNameLower):
				confidence = 0.80
			default:
				continue
			}

			edges = append(edges, &store.Edge{
				Project:  p.ProjectName,
				SourceID: source.ID,
				TargetID: dep.node.ID,
				Type:     "CONFIGURES",
				Properties: map[string]any{
					"strategy":        "dependency_import",
					"confidence_tier": store.ConfidenceAmbiguous,
					"confidence":      confidence,
					"dep_name":        dep.name,
				},
			})
		}
	}
	return edges
}

// isDependencyChild checks if a Variable node's QN suggests it's under a dependency section.
func isDependencyChild(v *store.Node) bool {
	parts := strings.Split(v.QualifiedName, ".")
	for _, p := range parts {
		pLower := strings.ToLower(p)
		if depSectionNames[pLower] {
			return true
		}
	}
	return false
}

// --- Strategy 3: Config File Path → Code String Reference ---

// matchConfigFileRefs scans source code for string literals referencing config files.
func (p *Pipeline) matchConfigFileRefs() []*store.Edge {
	// Collect config Module nodes
	modules, err := p.Store.FindNodesByLabel(p.ProjectName, "Module")
	if err != nil {
		return nil
	}

	configModules := make(map[string]*store.Node)     // basename → Module
	configModulesFull := make(map[string]*store.Node) // relPath → Module
	for _, m := range modules {
		if hasConfigExtension(m.FilePath) {
			configModules[filepath.Base(m.FilePath)] = m
			configModulesFull[m.FilePath] = m
		}
	}
	if len(configModules) == 0 {
		return nil
	}

	// Scan source files for config file references (use Module nodes from DB)
	var edges []*store.Edge
	for _, m := range modules {
		relPath := m.FilePath
		if hasConfigExtension(relPath) {
			continue // Skip config files themselves
		}

		// Read source from disk for string literal scanning
		fullPath := filepath.Join(p.RepoPath, relPath)
		source, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		matches := configFileRefRe.FindAllStringSubmatch(string(source), -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			refPath := match[1]

			// Try full path match first
			var target *store.Node
			var confidence float64
			if m, ok := configModulesFull[refPath]; ok {
				target = m
				confidence = 0.90
			} else {
				// Try basename match
				refBase := filepath.Base(refPath)
				if m, ok := configModules[refBase]; ok {
					target = m
					confidence = 0.70
				}
			}
			if target == nil {
				continue
			}

			// Find the source module/function
			moduleQN := moduleQNForFile(p.ProjectName, relPath)
			sourceNode, err := p.Store.FindNodeByQN(p.ProjectName, moduleQN)
			if err != nil || sourceNode == nil {
				continue
			}

			edges = append(edges, &store.Edge{
				Project:  p.ProjectName,
				SourceID: sourceNode.ID,
				TargetID: target.ID,
				Type:     "CONFIGURES",
				Properties: map[string]any{
					"strategy":        "file_reference",
					"confidence_tier": store.ConfidenceAmbiguous,
					"confidence":      confidence,
					"ref_path":        refPath,
				},
			})
		}
	}
	return edges
}

// buildEnvReaders snapshots env var access data from the extraction cache
// into p.envReaders. Must be called before extractionCache is released.
func (p *Pipeline) buildEnvReaders() {
	readers := make(map[string][]string)
	for _, ext := range p.extractionCache {
		if ext.Result == nil {
			continue
		}
		for _, ea := range ext.Result.EnvAccesses {
			if ea.EnvKey != "" && ea.EnclosingFuncQN != "" {
				readers[ea.EnvKey] = append(readers[ea.EnvKey], ea.EnclosingFuncQN)
			}
		}
	}
	p.envReaders = readers
	if len(readers) > 0 {
		slog.Info("configlinker.env_readers", "keys", len(readers))
	}
}

// --- Strategy 4: Terraform HCL Env Vars → Code Env Access ---

// reTFEnvName matches `name = "ENV_VAR"` or `"name" = "ENV_VAR"` patterns inside
// environment blocks in Terraform HCL files (e.g., container_definitions jsonencode).
var reTFEnvName = regexp.MustCompile(`(?:"name"|name)\s*[=:]\s*"([A-Z][A-Z0-9_]*)"`)

// reTFEnvBlock matches the start of an environment block or array in HCL.
var reTFEnvBlock = regexp.MustCompile(`(?i)environment\s*[=:]\s*\[`)

// extractTerraformEnvVars extracts environment variable names from a Terraform file.
// It looks for `environment` blocks (typically inside container_definitions jsonencode)
// and extracts `name = "VAR_NAME"` patterns from within those blocks.
func extractTerraformEnvVars(content string) []string {
	var envVars []string
	seen := make(map[string]bool)

	// Strategy: find `environment` array blocks and extract name fields within them.
	// This handles the common ECS pattern:
	//   container_definitions = jsonencode([{
	//     environment = [
	//       { name = "REDIS_URL", value = "redis://cache:6379" },
	//     ]
	//   }])
	locs := reTFEnvBlock.FindAllStringIndex(content, -1)
	for _, loc := range locs {
		// Start scanning from the opening bracket of the environment array
		start := loc[1] - 1 // position of '['
		depth := 0
		end := len(content)

		// Find the matching closing bracket
		for i := start; i < len(content); i++ {
			switch content[i] {
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					end = i + 1
					goto found
				}
			}
		}
	found:
		block := content[start:end]
		matches := reTFEnvName.FindAllStringSubmatch(block, -1)
		for _, m := range matches {
			if len(m) >= 2 && !seen[m[1]] {
				seen[m[1]] = true
				envVars = append(envVars, m[1])
			}
		}
	}

	return envVars
}

// matchTerraformEnvVars links Terraform HCL env var definitions to functions
// that read those env vars via os.environ/os.getenv/etc.
// Uses p.envReaders which was built during the pre-flush semantic pass.
func (p *Pipeline) matchTerraformEnvVars() []*store.Edge {
	if len(p.envReaders) == 0 {
		return nil
	}

	// Walk the repo looking for .tf files and extract env var names
	type tfEnvFile struct {
		relPath string
		envVars []string
	}
	var tfFiles []tfEnvFile

	_ = filepath.Walk(p.RepoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if discover.IGNORE_PATTERNS[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(info.Name()) != ".tf" {
			return nil
		}
		if info.Size() > 1<<20 {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil //nolint:nilerr // WHY: WalkDir callback - skip unreadable files
		}

		envVars := extractTerraformEnvVars(string(data))
		if len(envVars) > 0 {
			relPath, _ := filepath.Rel(p.RepoPath, path)
			relPath = filepath.ToSlash(relPath)
			tfFiles = append(tfFiles, tfEnvFile{relPath: relPath, envVars: envVars})
		}
		return nil
	})

	if len(tfFiles) == 0 {
		return nil
	}

	// For each TF file, find the InfraFile node (or Module node) and create edges
	var edges []*store.Edge
	for _, tf := range tfFiles {
		// Try to find the InfraFile node for this .tf file
		infraQN := p.infraQN(tf.relPath, map[string]any{"infra_type": "terraform"})
		sourceNode, err := p.Store.FindNodeByQN(p.ProjectName, infraQN)
		if err != nil || sourceNode == nil {
			continue
		}

		for _, envVar := range tf.envVars {
			funcQNs, ok := p.envReaders[envVar]
			if !ok {
				continue
			}
			for _, funcQN := range funcQNs {
				targetNode, findErr := p.Store.FindNodeByQN(p.ProjectName, funcQN)
				if findErr != nil || targetNode == nil {
					continue
				}

				edges = append(edges, &store.Edge{
					Project:  p.ProjectName,
					SourceID: sourceNode.ID,
					TargetID: targetNode.ID,
					Type:     "CONFIGURES",
					Properties: map[string]any{
						"strategy":        "terraform_env",
						"confidence_tier": store.ConfidenceAmbiguous,
						"confidence":      0.90,
						"env_key":         envVar,
					},
				})
			}
		}
	}

	return edges
}

// --- Helpers ---

// hasConfigExtension checks if a file path has a config file extension.
func hasConfigExtension(filePath string) bool {
	ext := filepath.Ext(filePath)
	return configExtensions[ext]
}

// moduleQNForFile computes the Module QN for a given file.
func moduleQNForFile(project, relPath string) string {
	// Strip extension, replace / with .
	noExt := strings.TrimSuffix(relPath, filepath.Ext(relPath))
	parts := strings.Split(noExt, "/")

	// Filter empty and special parts
	var filtered []string
	for _, p := range parts {
		if p != "" && p != "__init__" && p != "index" {
			filtered = append(filtered, p)
		}
	}

	return project + "." + strings.Join(filtered, ".")
}
