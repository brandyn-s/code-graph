package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

func assertTypeScriptImport(
	t *testing.T,
	dir string,
	sourceSuffix string,
	targetSuffix string,
) {
	t.Helper()
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := New(context.Background(), s, dir, discover.ModeFull)
	if err := p.Run(); err != nil {
		t.Fatalf("Pipeline.Run: %v", err)
	}

	sourceQN := p.ProjectName + sourceSuffix
	targetQN := p.ProjectName + targetSuffix
	edges := importsFrom(t, s, p.ProjectName, sourceQN)
	for _, edge := range edges {
		if importTargetQN(s, edge) == targetQN {
			return
		}
	}
	t.Fatalf(
		"expected IMPORTS edge from %s to %s; got %d; extracted imports=%v",
		sourceQN, targetQN, len(edges), p.importMaps[sourceQN],
	)
}

// TypeScript's NodeNext resolver intentionally maps a source import ending in
// `.js` to the corresponding `.ts` source file. The independent compiler oracle
// emits this as src/main.ts -> src/math.ts; the graph must use the same
// project-local file pair for its module-level IMPORTS edge.
func TestTypeScriptImports_NodeNextSourceExtensionResolution(t *testing.T) {
	dir, err := os.MkdirTemp("", "cgm-ts-imports-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	writeFile(t, filepath.Join(dir, "src", "math.ts"), `
export function normalize(value: number): number {
    return value;
}
`)
	writeFile(t, filepath.Join(dir, "src", "main.ts"), `
import { normalize } from "./math.js";

export function render(value: number): number {
    return normalize(value);
}
`)

	assertTypeScriptImport(t, dir, ".src.main", ".src.math")
}

func TestTypeScriptImports_ReExportResolution(t *testing.T) {
	dir, err := os.MkdirTemp("", "cgm-ts-reexports-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	writeFile(t, filepath.Join(dir, "src", "math.ts"), `
export function normalize(value: number): number {
    return value;
}
`)
	writeFile(t, filepath.Join(dir, "src", "index.ts"), `
export { normalize } from "./math.js";
`)

	assertTypeScriptImport(t, dir, ".src", ".src.math")
}

func TestTypeScriptImports_RootRelativeSpecifierResolution(t *testing.T) {
	dir, err := os.MkdirTemp("", "cgm-ts-root-imports-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	writeFile(t, filepath.Join(dir, "src", "components", "thing.tsx"), `
export function Thing(): null {
    return null;
}
`)
	writeFile(t, filepath.Join(dir, "src", "App.tsx"), `
import { Thing } from "components/thing";

export function App(): null {
    Thing();
    return null;
}
`)

	assertTypeScriptImport(t, dir, ".src.App", ".src.components.thing")
}

func TestTypeScriptImports_IncrementalDependentMatchesFullGraph(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "src", "icons", "Descope.tsx"), `
export function Descope(): null { return null; }
`)
	writeFile(t, filepath.Join(dir, "src", "ProviderButton.tsx"), `
import { Descope } from "./icons/Descope";

export function renderProviderIcon(): null {
    return Descope();
}
`)

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := New(context.Background(), s, dir, discover.ModeFull)
	if err := p.Run(); err != nil {
		t.Fatalf("initial Pipeline.Run: %v", err)
	}
	beforeNodes, beforeEdges := canonicalGraph(t, s, p.ProjectName)

	target := filepath.Join(dir, "src", "icons", "Descope.tsx")
	source, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- target is a fixed child of the test-owned TempDir.
	if err := os.WriteFile(target, append(source, []byte("\n// comment only\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	p = New(context.Background(), s, dir, discover.ModeFull)
	if err := p.Run(); err != nil {
		t.Fatalf("incremental Pipeline.Run: %v", err)
	}
	afterNodes, afterEdges := canonicalGraph(t, s, p.ProjectName)
	if !eq(beforeNodes, afterNodes) {
		t.Errorf("incremental TypeScript nodes changed:\n  %s", strings.Join(diff(beforeNodes, afterNodes), "\n  "))
	}
	if !eq(beforeEdges, afterEdges) {
		t.Errorf("incremental TypeScript edges changed:\n  %s", strings.Join(diff(beforeEdges, afterEdges), "\n  "))
	}
}
