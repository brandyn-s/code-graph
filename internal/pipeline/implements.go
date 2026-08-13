package pipeline

import (
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/fqn"
	"github.com/DeusData/codebase-memory-mcp/internal/lang"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// ifaceMethodInfo holds a method name and its qualified name for OVERRIDE edge creation.
type ifaceMethodInfo struct {
	name          string
	qualifiedName string
}

// ifaceInfo holds an interface node and its required methods.
type ifaceInfo struct {
	node    *store.Node
	methods []ifaceMethodInfo
}

// passImplements detects interface satisfaction and creates IMPLEMENTS edges.
// Supports Go (implicit, method-set matching) and explicit implements for
// TypeScript, Java, C#, Kotlin, Scala, and Rust.
func (p *Pipeline) passImplements() {
	slog.Info("pass5.implements")

	var linkCount, overrideCount int

	// Go: implicit interface satisfaction (existing)
	l, o := p.implementsGo()
	linkCount += l
	overrideCount += o

	// Explicit implements/extends via CBM base_classes data
	l, o = p.implementsExplicitCBM()
	linkCount += l
	overrideCount += o

	// Rust: impl Trait for Struct
	l, o = p.implementsRust()
	linkCount += l
	overrideCount += o

	slog.Info("pass5.implements.done", "links", linkCount, "overrides", overrideCount)
}

// implementsGo handles Go's implicit interface satisfaction via method sets.
func (p *Pipeline) implementsGo() (linkCount, overrideCount int) {
	ifaces := p.collectGoInterfaces()
	if len(ifaces) == 0 {
		return 0, 0
	}

	structMethods, structQNPrefix := p.collectStructMethods()
	return p.matchImplements(ifaces, structMethods, structQNPrefix)
}

// collectGoInterfaces returns Go interfaces with their method names.
func (p *Pipeline) collectGoInterfaces() []ifaceInfo {
	interfaces, findErr := p.findNodesByLabel(p.ProjectName, "Interface")
	if findErr != nil || len(interfaces) == 0 {
		return nil
	}

	var ifaces []ifaceInfo
	for _, iface := range interfaces {
		if !strings.HasSuffix(iface.FilePath, ".go") {
			continue
		}

		edges, edgeErr := p.findEdgesBySourceAndType(iface.ID, "DEFINES_METHOD")
		if edgeErr != nil || len(edges) == 0 {
			continue
		}

		var methods []ifaceMethodInfo
		for _, e := range edges {
			methodNode, _ := p.findNodeByID(e.TargetID)
			if methodNode != nil {
				methods = append(methods, ifaceMethodInfo{
					name:          methodNode.Name,
					qualifiedName: methodNode.QualifiedName,
				})
			}
		}

		if len(methods) > 0 {
			ifaces = append(ifaces, ifaceInfo{node: iface, methods: methods})
		}
	}
	return ifaces
}

// collectStructMethods builds maps of receiver type -> method names and QN prefixes
// from Go methods with receiver properties.
func (p *Pipeline) collectStructMethods() (structMethods map[string]map[string]bool, structQNPrefix map[string]string) {
	methodNodes, findErr := p.findNodesByLabel(p.ProjectName, "Method")
	if findErr != nil {
		return nil, nil
	}

	structMethods = make(map[string]map[string]bool)
	structQNPrefix = make(map[string]string)

	for _, m := range methodNodes {
		if !strings.HasSuffix(m.FilePath, ".go") {
			continue
		}
		recv, ok := m.Properties["receiver"]
		if !ok {
			continue
		}
		recvStr, ok := recv.(string)
		if !ok || recvStr == "" {
			continue
		}

		typeName := extractReceiverType(recvStr)
		if typeName == "" {
			continue
		}

		if structMethods[typeName] == nil {
			structMethods[typeName] = make(map[string]bool)
		}
		structMethods[typeName][m.Name] = true

		if _, exists := structQNPrefix[typeName]; !exists {
			if idx := strings.LastIndex(m.QualifiedName, "."); idx > 0 {
				structQNPrefix[typeName] = m.QualifiedName[:idx]
			}
		}
	}
	return
}

// matchImplements checks each struct against each interface and creates IMPLEMENTS + OVERRIDE edges.
func (p *Pipeline) matchImplements(
	ifaces []ifaceInfo,
	structMethods map[string]map[string]bool,
	structQNPrefix map[string]string,
) (linkCount, overrideCount int) {
	for _, iface := range ifaces {
		for typeName, methodSet := range structMethods {
			if !satisfies(iface.methods, methodSet) {
				continue
			}

			structNode := p.findStructNode(typeName, structQNPrefix)
			if structNode == nil {
				continue
			}

			_ = p.insertEdge(&store.Edge{
				Project:  p.ProjectName,
				SourceID: structNode.ID,
				TargetID: iface.node.ID,
				Type:     "IMPLEMENTS",
				Properties: map[string]any{
					"confidence_tier": store.ConfidenceInferred,
				},
			})
			linkCount++

			overrideCount += p.createOverrideEdges(iface.methods, typeName, structQNPrefix)
		}
	}
	return linkCount, overrideCount
}

// createOverrideEdges creates OVERRIDE edges from struct methods to interface methods.
func (p *Pipeline) createOverrideEdges(
	ifaceMethods []ifaceMethodInfo,
	typeName string,
	structQNPrefix map[string]string,
) int {
	prefix, ok := structQNPrefix[typeName]
	if !ok {
		return 0
	}

	count := 0
	for _, im := range ifaceMethods {
		structMethodQN := prefix + "." + im.name
		structMethodNode, _ := p.findNodeByQN(p.ProjectName, structMethodQN)
		if structMethodNode == nil {
			continue
		}

		ifaceMethodNode, _ := p.findNodeByQN(p.ProjectName, im.qualifiedName)
		if ifaceMethodNode == nil {
			continue
		}

		_ = p.insertEdge(&store.Edge{
			Project:  p.ProjectName,
			SourceID: structMethodNode.ID,
			TargetID: ifaceMethodNode.ID,
			Type:     "OVERRIDE",
			Properties: map[string]any{
				"confidence_tier": store.ConfidenceInferred,
			},
		})
		count++
	}
	return count
}

// findStructNode looks up the struct/class node for a given receiver type name.
func (p *Pipeline) findStructNode(typeName string, structQNPrefix map[string]string) *store.Node {
	if prefix, ok := structQNPrefix[typeName]; ok {
		structQN := prefix + "." + typeName
		if n, _ := p.findNodeByQN(p.ProjectName, structQN); n != nil {
			return n
		}
	}

	classes, _ := p.findNodesByLabel(p.ProjectName, "Class")
	for _, c := range classes {
		if c.Name == typeName && strings.HasSuffix(c.FilePath, ".go") {
			return c
		}
	}
	return nil
}

// extractReceiverType extracts the type name from a Go receiver string.
// "(h *Handlers)" -> "Handlers", "(s Store)" -> "Store"
func extractReceiverType(recv string) string {
	recv = strings.TrimSpace(recv)
	recv = strings.Trim(recv, "()")
	parts := strings.Fields(recv)
	if len(parts) == 0 {
		return ""
	}
	// Last field is the type, possibly with * prefix
	typeName := parts[len(parts)-1]
	typeName = strings.TrimPrefix(typeName, "*")
	return typeName
}

// satisfies checks if a set of method names includes all interface methods.
func satisfies(ifaceMethods []ifaceMethodInfo, structMethodSet map[string]bool) bool {
	for _, m := range ifaceMethods {
		if !structMethodSet[m.name] {
			return false
		}
	}
	return true
}

// --- Explicit implements via CBM base_classes data ---

// explicitImplementsExts maps languages to the extensions to check.
var explicitImplementsExts = map[lang.Language]string{
	lang.TypeScript: ".ts",
	lang.TSX:        ".tsx",
	lang.Java:       ".java",
}

// implementsExplicitCBM detects explicit implements/extends relationships
// using CBM-extracted base_classes data from Class/Interface nodes.
func (p *Pipeline) implementsExplicitCBM() (linkCount, overrideCount int) {
	for _, label := range []string{"Class", "Interface"} {
		nodes, err := p.findNodesByLabel(p.ProjectName, label)
		if err != nil {
			continue
		}
		for _, classNode := range nodes {
			lc, oc := p.processExplicitBases(classNode)
			linkCount += lc
			overrideCount += oc
		}
	}
	return
}

// processExplicitBases links one class/interface node to all its base types.
func (p *Pipeline) processExplicitBases(classNode *store.Node) (linkCount, overrideCount int) {
	ext := strings.ToLower(filepath.Ext(classNode.FilePath))
	fileLang, hasLang := lang.LanguageForExtension(ext)
	if !hasLang {
		return
	}
	if _, isExplicit := explicitImplementsExts[fileLang]; !isExplicit {
		return
	}

	property := "base_classes"
	if fileLang == lang.TypeScript || fileLang == lang.TSX {
		property = "implements_types"
	}
	baseClasses, ok := classNode.Properties[property]
	if !ok {
		return
	}
	baseList, ok := baseClasses.([]any)
	if !ok {
		return
	}

	moduleQN := fqn.ModuleQN(p.ProjectName, classNode.FilePath)
	importMap := p.importMaps[moduleQN]

	for _, bc := range baseList {
		baseName, ok := bc.(string)
		if !ok || baseName == "" {
			continue
		}
		ifaceQN := resolveAsClass(baseName, p.registry, moduleQN, importMap)
		if ifaceQN == "" {
			continue
		}
		ifaceNode, _ := p.findNodeByQN(p.ProjectName, ifaceQN)
		if ifaceNode == nil {
			continue
		}
		_ = p.insertEdge(&store.Edge{
			Project:  p.ProjectName,
			SourceID: classNode.ID,
			TargetID: ifaceNode.ID,
			Type:     "IMPLEMENTS",
			Properties: map[string]any{
				"confidence_tier": store.ConfidenceInferred,
			},
		})
		linkCount++
		overrideCount += p.createOverrideEdgesExplicit(classNode, ifaceNode)
	}
	return
}

// createOverrideEdgesExplicit creates OVERRIDE edges by matching method names
// between a class and an interface.
func (p *Pipeline) createOverrideEdgesExplicit(classNode, ifaceNode *store.Node) int {
	// Get interface methods
	ifaceEdges, err := p.findEdgesBySourceAndType(ifaceNode.ID, "DEFINES_METHOD")
	if err != nil || len(ifaceEdges) == 0 {
		return 0
	}

	// Get class methods
	classEdges, err := p.findEdgesBySourceAndType(classNode.ID, "DEFINES_METHOD")
	if err != nil || len(classEdges) == 0 {
		return 0
	}

	// Build class method name -> node ID map
	classMethodByName := make(map[string]int64)
	for _, e := range classEdges {
		methodNode, _ := p.findNodeByID(e.TargetID)
		if methodNode != nil {
			classMethodByName[methodNode.Name] = methodNode.ID
		}
	}

	count := 0
	for _, e := range ifaceEdges {
		ifaceMethodNode, _ := p.findNodeByID(e.TargetID)
		if ifaceMethodNode == nil {
			continue
		}
		// Constructors initialize one concrete class; they do not participate
		// in method override/implementation relationships.
		if ifaceMethodNode.Name == "constructor" {
			continue
		}
		classMethodID, ok := classMethodByName[ifaceMethodNode.Name]
		if !ok {
			continue
		}

		_ = p.insertEdge(&store.Edge{
			Project:  p.ProjectName,
			SourceID: classMethodID,
			TargetID: ifaceMethodNode.ID,
			Type:     "OVERRIDE",
			Properties: map[string]any{
				"confidence_tier": store.ConfidenceInferred,
			},
		})
		count++
	}
	return count
}

// --- Rust: impl Trait for Struct ---

// implementsRust detects `impl Trait for Struct` from CBM extraction data.
//
// Phase C instrumentation (2026-05-07): when this pass runs on a repo
// where the IMPLEMENTS count appears suspiciously low, we want to know
// WHICH stage of the resolve→lookup→emit pipeline is dropping
// candidates. Each `continue` path now bumps a per-reason counter, and
// the caller logs an aggregate summary. Diagnoses three distinct
// failure shapes in one pass without re-indexing:
//
//   - traitQN-empty: name didn't resolve to a class-like registry entry
//     (registry doesn't know about the trait, or the label allowlist
//     in `resolveAsClass` rejects its label)
//   - structQN-empty: same as above for the struct side
//   - trait-node-nil / struct-node-nil: QN resolved but no node in the
//     graph DB (DEFINES/upsert ran out of scope, or DB inconsistency)
//
// On PSM 2026-05-07 baseline (365 IMPLEMENTS edges across 2,065 Rust
// files), the conjecture was that A2's label-set expansion would lift
// the count substantially. It didn't — count stayed at 365. The
// instrumentation proves whether 365 is the true ceiling (zero-skip
// run), or whether resolver gaps are dropping legitimate impl-blocks
// silently.
func (p *Pipeline) implementsRust() (linkCount, overrideCount int) {
	implBlocksSeen := 0
	skipReasons := map[string]int{}
	bumpSkip := func(reason string) {
		skipReasons[reason]++
	}

	for relPath, ext := range p.extractionCache {
		if ext.Language != lang.Rust || ext.Result == nil {
			continue
		}
		if len(ext.Result.ImplTraits) == 0 {
			continue
		}

		moduleQN := fqn.ModuleQN(p.ProjectName, relPath)
		importMap := p.importMaps[moduleQN]

		for _, it := range ext.Result.ImplTraits {
			implBlocksSeen++
			traitQN, traitReason := resolveAsClassWithReason(it.TraitName, p.registry, moduleQN, importMap)
			if traitQN == "" {
				// Phase D-Instrument (2026-05-08): split previously-aggregate
				// traitQN-empty into 3 sub-reasons (resolve-empty / label-missing /
				// label-mismatch) so the dominant failure mode drives D-Implement.
				skipKey := "traitQN-empty:" + string(traitReason)
				bumpSkip(skipKey)
				slog.Debug("implementsRust.skip",
					"reason", skipKey,
					"module", moduleQN,
					"trait", it.TraitName,
					"struct", it.StructName,
				)
				continue
			}
			// Phase A (2026-05-08): track resolveAsClassWithReason fallback
			// hits — successful resolutions that ONLY happened because the
			// new label-aware fallback fired. Lets the summary attribute
			// emitted-IMPLEMENTS-edges-rescued back to the fallback strategy
			// vs the existing 9 strategies.
			if traitReason == ResolveOKViaFallbackFromEmpty || traitReason == ResolveOKViaFallbackFromMismatch {
				bumpSkip("traitQN-rescued:" + string(traitReason))
			}
			structQN, structReason := resolveAsClassWithReason(it.StructName, p.registry, moduleQN, importMap)
			if structQN == "" {
				skipKey := "structQN-empty:" + string(structReason)
				bumpSkip(skipKey)
				slog.Debug("implementsRust.skip",
					"reason", skipKey,
					"module", moduleQN,
					"trait", it.TraitName,
					"struct", it.StructName,
				)
				continue
			}
			if structReason == ResolveOKViaFallbackFromEmpty || structReason == ResolveOKViaFallbackFromMismatch {
				bumpSkip("structQN-rescued:" + string(structReason))
			}

			traitDBNode, _ := p.findNodeByQN(p.ProjectName, traitQN)
			if traitDBNode == nil {
				// Phase A2 layer-2 fix (2026-05-08, plan #459 / PR #266
				// follow-up): if the resolver returned a synthetic external
				// trait QN (`_external.<crate>.<trait>`), there's no graph
				// node by design — std/external crates aren't indexed.
				// Upsert a synthetic Interface node on demand so emitImpl
				// can wire the IMPLEMENTS edge.
				//
				// Without this fix, the synthetic-QN approach silently
				// drops 640 of 722 PSM resolve-empty cases at trait-node-nil
				// (verified empirically in PR #266 first re-index).
				if strings.HasPrefix(traitQN, "_external.") {
					traitName := traitQN
					if idx := strings.LastIndex(traitName, "."); idx >= 0 {
						traitName = traitName[idx+1:]
					}
					if err := p.upsertNode(&store.Node{
						Project:       p.ProjectName,
						Label:         "Interface",
						Name:          traitName,
						QualifiedName: traitQN,
						Properties: map[string]any{
							"synthetic":  true,
							"source":     "SyntheticInterfaceRegistry",
							"definition": "external",
						},
					}); err == nil {
						traitDBNode, _ = p.findNodeByQN(p.ProjectName, traitQN)
					}
				}
				if traitDBNode == nil {
					bumpSkip("trait-node-nil")
					slog.Debug("implementsRust.skip",
						"reason", "trait-node-nil",
						"trait_qn", traitQN,
						"struct_qn", structQN,
					)
					continue
				}
				bumpSkip("traitQN-rescued:synthetic-node-upsert")
			}
			structDBNode, _ := p.findNodeByQN(p.ProjectName, structQN)
			if structDBNode == nil {
				// Phase A2 struct-side mirror (2026-05-08, plan #459
				// follow-up to PR #267): when the resolver returned a
				// synthetic external struct QN (`_external.<crate>.<struct>`),
				// upsert a synthetic Class node on demand so emitImpl can
				// wire the IMPLEMENTS edge. Mirror of the trait-side fix
				// in PR #267, applied to the struct side.
				if strings.HasPrefix(structQN, "_external.") {
					structName := structQN
					if idx := strings.LastIndex(structName, "."); idx >= 0 {
						structName = structName[idx+1:]
					}
					if err := p.upsertNode(&store.Node{
						Project:       p.ProjectName,
						Label:         "Class",
						Name:          structName,
						QualifiedName: structQN,
						Properties: map[string]any{
							"synthetic":  true,
							"source":     "SyntheticStructRegistry",
							"definition": "external",
						},
					}); err == nil {
						structDBNode, _ = p.findNodeByQN(p.ProjectName, structQN)
					}
				}
				if structDBNode == nil {
					bumpSkip("struct-node-nil")
					slog.Debug("implementsRust.skip",
						"reason", "struct-node-nil",
						"trait_qn", traitQN,
						"struct_qn", structQN,
					)
					continue
				}
				bumpSkip("structQN-rescued:synthetic-node-upsert")
			}

			_ = p.insertEdge(&store.Edge{
				Project:  p.ProjectName,
				SourceID: structDBNode.ID,
				TargetID: traitDBNode.ID,
				Type:     "IMPLEMENTS",
				Properties: map[string]any{
					"confidence_tier": store.ConfidenceInferred,
				},
			})
			linkCount++

			overrideCount += p.createOverrideEdgesExplicit(structDBNode, traitDBNode)
		}
	}

	totalSkipped := 0
	for _, n := range skipReasons {
		totalSkipped += n
	}
	slog.Info("implementsRust.summary",
		"impl_blocks_seen", implBlocksSeen,
		"emitted", linkCount,
		"skipped", totalSkipped,
		"reasons", skipReasons,
	)
	return
}
