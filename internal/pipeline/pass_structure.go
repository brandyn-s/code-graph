// Structure pass: directory, package, and file nodes with CONTAINS edges.
//
// Split from pipeline.go without behaviour changes.
package pipeline

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/brandyn-s/code-graph/internal/cbm"
	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/fqn"
	"github.com/brandyn-s/code-graph/internal/lang"
	"github.com/brandyn-s/code-graph/internal/store"
)

// passStructure creates Project, Folder, Package, File nodes and containment edges.
// Collects all nodes/edges in memory first, then batch-writes to DB.
func (p *Pipeline) passStructure(files []discover.FileInfo) error {
	slog.Info("pass1.structure")

	dirSet, dirIsPackage := p.classifyDirectories(files)

	nodes := make([]*store.Node, 0, len(files)*2)
	edges := make([]pendingEdge, 0, len(files)*2)

	projectQN := p.ProjectName
	nodes = append(nodes, &store.Node{
		Project:       p.ProjectName,
		Label:         "Project",
		Name:          p.ProjectName,
		QualifiedName: projectQN,
	})

	dirNodes, dirEdges := p.buildDirNodesEdges(dirSet, dirIsPackage, projectQN)
	nodes = append(nodes, dirNodes...)
	edges = append(edges, dirEdges...)

	fileNodes, fileEdges := p.buildFileNodesEdges(files)
	nodes = append(nodes, fileNodes...)
	edges = append(edges, fileEdges...)

	return p.batchWriteStructure(nodes, edges)
}

// classifyDirectories collects all directories and determines which are packages.
func (p *Pipeline) classifyDirectories(files []discover.FileInfo) (allDirs, packageDirs map[string]bool) {
	packageIndicators := make(map[string]bool)
	for _, l := range lang.AllLanguages() {
		spec := lang.ForLanguage(l)
		if spec != nil {
			for _, pi := range spec.PackageIndicators {
				packageIndicators[pi] = true
			}
		}
	}

	allDirs = make(map[string]bool)
	for _, f := range files {
		dir := filepath.Dir(f.RelPath)
		for dir != "." && dir != "" && !allDirs[dir] {
			allDirs[dir] = true
			dir = filepath.Dir(dir)
		}
	}

	packageDirs = make(map[string]bool, len(allDirs))
	for dir := range allDirs {
		absDir := filepath.Join(p.RepoPath, dir)
		for indicator := range packageIndicators {
			if _, err := os.Stat(filepath.Join(absDir, indicator)); err == nil {
				packageDirs[dir] = true
				break
			}
		}
	}
	return
}

func (p *Pipeline) buildDirNodesEdges(dirSet, dirIsPackage map[string]bool, projectQN string) ([]*store.Node, []pendingEdge) {
	nodes := make([]*store.Node, 0, len(dirSet))
	edges := make([]pendingEdge, 0, len(dirSet))

	for dir := range dirSet {
		label := "Folder"
		edgeType := "CONTAINS_FOLDER"
		if dirIsPackage[dir] {
			label = "Package"
			edgeType = "CONTAINS_PACKAGE"
		}
		qn := fqn.FolderQN(p.ProjectName, dir)
		nodes = append(nodes, &store.Node{
			Project:       p.ProjectName,
			Label:         label,
			Name:          filepath.Base(dir),
			QualifiedName: qn,
			FilePath:      dir,
		})

		parent := filepath.Dir(dir)
		parentQN := projectQN
		if parent != "." && parent != "" {
			parentQN = fqn.FolderQN(p.ProjectName, parent)
		}
		edges = append(edges, pendingEdge{SourceQN: parentQN, TargetQN: qn, Type: edgeType})
	}
	return nodes, edges
}

func (p *Pipeline) buildFileNodesEdges(files []discover.FileInfo) ([]*store.Node, []pendingEdge) {
	nodes := make([]*store.Node, 0, len(files))
	edges := make([]pendingEdge, 0, len(files))

	for _, f := range files {
		fileQN := fqn.Compute(p.ProjectName, f.RelPath, "") + ".__file__"
		fileProps := map[string]any{
			"extension": filepath.Ext(f.RelPath),
			"is_test":   isTestFile(f.RelPath, f.Language),
		}
		if f.Language != "" {
			fileProps["language"] = string(f.Language)
		}
		nodes = append(nodes, &store.Node{
			Project:       p.ProjectName,
			Label:         "File",
			Name:          filepath.Base(f.RelPath),
			QualifiedName: fileQN,
			FilePath:      f.RelPath,
			Properties:    fileProps,
		})

		parentQN := p.dirQN(filepath.Dir(f.RelPath))
		edges = append(edges, pendingEdge{SourceQN: parentQN, TargetQN: fileQN, Type: store.EdgeContainsFile})
	}
	return nodes, edges
}

func (p *Pipeline) batchWriteStructure(nodes []*store.Node, edges []pendingEdge) error {
	// JS/TS index modules and Python __init__ modules intentionally share a QN
	// with their directory. On incremental runs the semantic Module already
	// exists in SQLite; a structural Folder/Package upsert must not demote it.
	// The full in-memory build retains its established later-write semantics.
	if p.buf == nil {
		filtered := nodes[:0]
		for _, n := range nodes {
			if n.Label == "Folder" || n.Label == "Package" {
				existing, err := p.Store.FindNodeByQN(p.ProjectName, n.QualifiedName)
				if err == nil && existing != nil && existing.Label == "Module" {
					continue
				}
			}
			filtered = append(filtered, n)
		}
		nodes = filtered
	}
	idMap, err := p.upsertNodeBatch(nodes)
	if err != nil {
		return fmt.Errorf("pass1 batch upsert: %w", err)
	}
	// Include preserved collision nodes in the edge endpoint map.
	if p.buf == nil {
		for _, edge := range edges {
			for _, qn := range []string{edge.SourceQN, edge.TargetQN} {
				if _, ok := idMap[qn]; ok {
					continue
				}
				if existing, findErr := p.Store.FindNodeByQN(p.ProjectName, qn); findErr == nil && existing != nil {
					idMap[qn] = existing.ID
				}
			}
		}
	}

	realEdges := make([]*store.Edge, 0, len(edges))
	for _, pe := range edges {
		srcID, srcOK := idMap[pe.SourceQN]
		tgtID, tgtOK := idMap[pe.TargetQN]
		if srcOK && tgtOK {
			realEdges = append(realEdges, &store.Edge{
				Project:    p.ProjectName,
				SourceID:   srcID,
				TargetID:   tgtID,
				Type:       pe.Type,
				Properties: pe.Properties,
			})
		}
	}

	if err := p.insertEdgeBatch(realEdges); err != nil {
		return fmt.Errorf("pass1 batch edges: %w", err)
	}
	return nil
}

func (p *Pipeline) dirQN(relDir string) string {
	if relDir == "." || relDir == "" {
		return p.ProjectName
	}
	return fqn.FolderQN(p.ProjectName, relDir)
}

// pendingEdge represents an edge to be created after batch node insertion,
// using qualified names that will be resolved to IDs.
type pendingEdge struct {
	SourceQN   string
	TargetQN   string
	Type       string
	Properties map[string]any
}

// parseResult holds the output of a pure file parse (no DB access).
type parseResult struct {
	File           discover.FileInfo
	Nodes          []*store.Node
	PendingEdges   []pendingEdge
	ImportMap      map[string]string
	ImportBindings map[string]string // Phase 3b: bare-name → import target path
	CBMResult      *cbm.FileResult   // CBM extraction result (nil when using legacy AST path)
	Err            error
}

// progressTicker logs a progress line every 5 seconds for long-running parallel phases.
// Returns a stop function that must be called when the phase completes.
func progressTicker(project, phase string, counter *atomic.Int64, total, basePct int) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n := int(counter.Load())
				pct := basePct + (n*10)/total
				slog.Info("index.progress", "project", project, "phase", phase, "pct", pct, "detail", fmt.Sprintf("%d/%d files", n, total))
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}
