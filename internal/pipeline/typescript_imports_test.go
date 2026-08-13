package pipeline

import (
	"context"
	"os"
	"path/filepath"
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
