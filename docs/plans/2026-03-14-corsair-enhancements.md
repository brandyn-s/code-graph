# Corsair-Specific Enhancements Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add three Corsair-focused capabilities to codebase-memory-mcp: Nix derivation graph parsing, security-aware node tagging, and a security surfaces query tool for STIG integration.

**Architecture:** Each feature adds a new pipeline pass or tool following existing patterns. Feature 2 (Nix) adds a `parseFlakeLock` infra parser plus a `nix_inputs` architecture aspect. Feature 3 (security tagging) adds a post-flush enrichment pass that pattern-matches node names/decorators/paths to assign `security_role` properties. Feature 4 (STIG tool) adds a convenience `query_security_surfaces` MCP tool that wraps the tagged data, plus guidance for the external `/stig-assess` skill update.

**Tech Stack:** Go 1.26, SQLite, tree-sitter (Nix grammar already registered), JSON parsing for flake.lock

**Repo:** `C:/Users/user/Documents/GitHub/codebase-memory-mcp` (redacted-org fork)

**Dependencies:** Feature 4 depends on Feature 3. Features 2 and 3 are independent.

**Corsair context:** 647K LOC across Rust/TypeScript/Python/Nix. 44K nodes, 137K edges when indexed. NixOS fleet managed via flake.nix. Rust services use axum with middleware auth. STIG assessments done via `/stig-assess` skill in Claude Code.

---

## Feature 2: Nix Derivation Graph

### Task 1: Parse flake.lock into NixInput Nodes + DEPENDS_ON Edges

**Finding:** Corsair's `flake.lock` defines all Nix input dependencies as a JSON structure. The pipeline's `infrascan.go` already routes files to parsers by name/extension but has no Nix handler.

**Files:**
- Modify: `internal/pipeline/infrascan.go` (add routing + parser)
- Test: `internal/pipeline/infrascan_test.go`

**Step 1: Write the failing test**

Add to `internal/pipeline/infrascan_test.go`:

```go
func TestParseFlakeLock(t *testing.T) {
	dir := t.TempDir()
	flakeLock := filepath.Join(dir, "flake.lock")

	content := `{
  "nodes": {
    "nixpkgs": {
      "locked": {
        "type": "github",
        "owner": "NixOS",
        "repo": "nixpkgs",
        "rev": "abc123def456",
        "lastModified": 1700000000
      },
      "original": {
        "type": "github",
        "owner": "NixOS",
        "repo": "nixpkgs"
      }
    },
    "rust-overlay": {
      "locked": {
        "type": "github",
        "owner": "oxalica",
        "repo": "rust-overlay",
        "rev": "789def012345"
      },
      "inputs": {
        "nixpkgs": "nixpkgs"
      },
      "original": {
        "type": "github",
        "owner": "oxalica",
        "repo": "rust-overlay"
      }
    },
    "root": {
      "inputs": {
        "nixpkgs": "nixpkgs",
        "rust-overlay": "rust-overlay"
      }
    }
  },
  "root": "root",
  "version": 7
}`

	if err := os.WriteFile(flakeLock, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	results := parseFlakeLock(flakeLock, "flake.lock")
	if len(results) == 0 {
		t.Fatal("expected infra nodes from flake.lock")
	}

	// Should have entries for nixpkgs and rust-overlay (not root)
	names := make(map[string]bool)
	for _, r := range results {
		names[r.properties["input_name"].(string)] = true
		if r.infraType != "nix-input" {
			t.Errorf("expected infraType=nix-input, got %s", r.infraType)
		}
	}
	if !names["nixpkgs"] {
		t.Error("expected nixpkgs in parsed inputs")
	}
	if !names["rust-overlay"] {
		t.Error("expected rust-overlay in parsed inputs")
	}
	if names["root"] {
		t.Error("root node should be excluded from inputs")
	}

	// Check nixpkgs properties
	for _, r := range results {
		if r.properties["input_name"] == "nixpkgs" {
			if r.properties["source_type"] != "github" {
				t.Errorf("expected source_type=github, got %v", r.properties["source_type"])
			}
			if r.properties["owner"] != "NixOS" {
				t.Errorf("expected owner=NixOS, got %v", r.properties["owner"])
			}
			if r.properties["repo"] != "nixpkgs" {
				t.Errorf("expected repo=nixpkgs, got %v", r.properties["repo"])
			}
			if r.properties["rev"] != "abc123def456" {
				t.Errorf("expected rev=abc123def456, got %v", r.properties["rev"])
			}
		}
	}
}

func TestParseFlakeLock_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	flakeLock := filepath.Join(dir, "flake.lock")
	if err := os.WriteFile(flakeLock, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := parseFlakeLock(flakeLock, "flake.lock")
	if len(results) != 0 {
		t.Errorf("expected 0 results for invalid JSON, got %d", len(results))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/pipeline/ -run TestParseFlakeLock -v`
Expected: FAIL (function doesn't exist)

**Step 3: Implement parseFlakeLock and wire it into the router**

Add to `internal/pipeline/infrascan.go`:

```go
// isFlakeLock returns true for Nix flake.lock files.
func isFlakeLock(name string) bool {
	return name == "flake.lock"
}

// parseFlakeLock parses a Nix flake.lock (JSON v7 format) and extracts
// input nodes with their source metadata (owner, repo, rev, type).
func parseFlakeLock(absPath, relPath string) []infraFile {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}

	var lock struct {
		Nodes map[string]struct {
			Locked *struct {
				Type         string `json:"type"`
				Owner        string `json:"owner"`
				Repo         string `json:"repo"`
				Rev          string `json:"rev"`
				LastModified int64  `json:"lastModified"`
			} `json:"locked"`
			Inputs   map[string]any `json:"inputs"`
			Original *struct {
				Type  string `json:"type"`
				Owner string `json:"owner"`
				Repo  string `json:"repo"`
			} `json:"original"`
		} `json:"nodes"`
		Root    string `json:"root"`
		Version int    `json:"version"`
	}

	if err := json.Unmarshal(data, &lock); err != nil {
		return nil
	}

	var results []infraFile
	for name, node := range lock.Nodes {
		if name == lock.Root {
			continue // skip the root node itself
		}
		props := map[string]any{
			"infra_type": "nix-input",
			"input_name": name,
		}
		if node.Locked != nil {
			props["source_type"] = node.Locked.Type
			if node.Locked.Owner != "" {
				props["owner"] = node.Locked.Owner
			}
			if node.Locked.Repo != "" {
				props["repo"] = node.Locked.Repo
			}
			if node.Locked.Rev != "" {
				props["rev"] = node.Locked.Rev
			}
		}
		// Record which other inputs this input depends on
		if len(node.Inputs) > 0 {
			deps := make([]string, 0, len(node.Inputs))
			for dep := range node.Inputs {
				deps = append(deps, dep)
			}
			props["depends_on"] = deps
		}
		results = append(results, infraFile{
			relPath:    relPath,
			infraType:  "nix-input",
			properties: props,
		})
	}
	return results
}
```

Add the JSON import to the file's import block: `"encoding/json"`.

Wire into `parseInfraFile`:

```go
// In parseInfraFile, add before the default case:
	case isFlakeLock(lower):
		return parseFlakeLock(absPath, relPath)
```

**Step 4: Run tests to verify**

Run: `go test ./internal/pipeline/ -run TestParseFlakeLock -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/pipeline/infrascan.go internal/pipeline/infrascan_test.go
git commit -m "feat: parse flake.lock into NixInput nodes

Extract Nix flake inputs from flake.lock (v7 format) as InfraFile
nodes with source metadata (owner, repo, rev, type). The root node
is excluded. Input-to-input dependencies stored in depends_on property.

Queryable via: search_graph(label='InfraFile', name_pattern='nixpkgs')
or query_graph with Cypher."
```

---

### Task 2: Create DEPENDS_ON Edges Between Nix Inputs

**Finding:** `parseFlakeLock` stores `depends_on` as a property list, but doesn't create graph edges. We need a post-parse step that resolves input names to node IDs and creates `DEPENDS_ON` edges.

**Files:**
- Modify: `internal/pipeline/infrascan.go` (add edge creation in `passInfraFiles`)
- Test: `internal/pipeline/infrascan_test.go`

**Step 1: Write the failing test**

Add to `internal/pipeline/infrascan_test.go`:

```go
func TestFlakeLockDependsOnEdges(t *testing.T) {
	// This is an integration test using a real pipeline store
	dir := t.TempDir()
	routerDir := filepath.Join(dir, "db")
	repoDir := filepath.Join(dir, "repo")

	if err := os.MkdirAll(repoDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Write a flake.lock where rust-overlay depends on nixpkgs
	flakeContent := `{
  "nodes": {
    "nixpkgs": {
      "locked": {"type": "github", "owner": "NixOS", "repo": "nixpkgs", "rev": "abc123"}
    },
    "rust-overlay": {
      "locked": {"type": "github", "owner": "oxalica", "repo": "rust-overlay", "rev": "def456"},
      "inputs": {"nixpkgs": "nixpkgs"}
    },
    "root": {
      "inputs": {"nixpkgs": "nixpkgs", "rust-overlay": "rust-overlay"}
    }
  },
  "root": "root",
  "version": 7
}`
	if err := os.WriteFile(filepath.Join(repoDir, "flake.lock"), []byte(flakeContent), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := store.OpenInDir(routerDir, "nix-test")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	p := New(context.Background(), st, repoDir, discover.ModeFull)
	if err := st.UpsertProject(p.ProjectName, repoDir); err != nil {
		t.Fatal(err)
	}

	p.passInfraFiles()

	// Check DEPENDS_ON edges exist
	edges, err := st.FindEdgesByType(p.ProjectName, "DEPENDS_ON")
	if err != nil {
		t.Fatalf("query edges: %v", err)
	}

	if len(edges) == 0 {
		t.Fatal("expected DEPENDS_ON edges from flake.lock parsing")
	}

	// rust-overlay should depend on nixpkgs
	found := false
	for _, e := range edges {
		srcNode, _ := st.FindNodeByID(e.SourceID)
		tgtNode, _ := st.FindNodeByID(e.TargetID)
		if srcNode != nil && tgtNode != nil {
			if srcNode.Properties["input_name"] == "rust-overlay" &&
				tgtNode.Properties["input_name"] == "nixpkgs" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected rust-overlay -> nixpkgs DEPENDS_ON edge")
	}
}
```

Note: This test requires `store.FindEdgesByType` which may not exist. If it doesn't, add a helper method to the store first:

```go
// In internal/store/edges.go:
func (s *Store) FindEdgesByType(project, edgeType string) ([]*Edge, error) {
	rows, err := s.q.Query(`SELECT id, project, source_id, target_id, type, properties
		FROM edges WHERE project=? AND type=?`, project, edgeType)
	if err != nil {
		return nil, fmt.Errorf("find edges by type: %w", err)
	}
	defer rows.Close()
	return scanEdges(rows)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/pipeline/ -run TestFlakeLockDependsOnEdges -v`
Expected: FAIL

**Step 3: Implement edge creation in passInfraFiles**

Add a second loop in `passInfraFiles` after the node creation loop. After the `upsertInfraNodes` loop:

```go
	// Create DEPENDS_ON edges for Nix flake inputs
	p.createNixDependsOnEdges(infras)
```

Then implement the method:

```go
// createNixDependsOnEdges resolves depends_on property lists into DEPENDS_ON edges
// between NixInput nodes from the same flake.lock.
func (p *Pipeline) createNixDependsOnEdges(infras []infraFile) {
	// Build index: input_name -> node QN for nix-input nodes
	inputQNs := make(map[string]string)
	for _, inf := range infras {
		if inf.infraType != "nix-input" {
			continue
		}
		name, _ := inf.properties["input_name"].(string)
		if name == "" {
			continue
		}
		inputQNs[name] = p.infraQN(inf.relPath, inf.properties)
	}

	if len(inputQNs) == 0 {
		return
	}

	// Resolve QNs to node IDs
	qns := make([]string, 0, len(inputQNs))
	for _, qn := range inputQNs {
		qns = append(qns, qn)
	}
	idMap, err := p.Store.FindNodeIDsByQNs(p.ProjectName, qns)
	if err != nil {
		slog.Warn("nix.depends_on.resolve", "err", err)
		return
	}

	// Create edges
	edgeCount := 0
	for _, inf := range infras {
		if inf.infraType != "nix-input" {
			continue
		}
		deps, ok := inf.properties["depends_on"].([]string)
		if !ok || len(deps) == 0 {
			continue
		}
		srcName, _ := inf.properties["input_name"].(string)
		srcQN := inputQNs[srcName]
		srcID, srcOK := idMap[srcQN]
		if !srcOK {
			continue
		}
		for _, dep := range deps {
			tgtQN, exists := inputQNs[dep]
			if !exists {
				continue
			}
			tgtID, tgtOK := idMap[tgtQN]
			if !tgtOK {
				continue
			}
			_, _ = p.Store.InsertEdge(&store.Edge{
				Project:  p.ProjectName,
				SourceID: srcID,
				TargetID: tgtID,
				Type:     "DEPENDS_ON",
			})
			edgeCount++
		}
	}
	if edgeCount > 0 {
		slog.Info("nix.depends_on", "edges", edgeCount)
	}
}
```

**Step 4: Run tests**

Run: `go test ./internal/pipeline/ -run "TestParseFlakeLock|TestFlakeLockDependsOnEdges" -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/pipeline/infrascan.go internal/pipeline/infrascan_test.go internal/store/edges.go
git commit -m "feat: create DEPENDS_ON edges between Nix flake inputs

Resolves the depends_on property from flake.lock parsing into graph
edges. Enables: query_graph('MATCH (a)-[:DEPENDS_ON]->(b) RETURN a.name, b.name')
to see the Nix dependency tree."
```

---

### Task 3: Add nix_inputs Architecture Aspect

**Files:**
- Modify: `internal/store/architecture.go` (add NixInput struct + query)
- Modify: `internal/tools/architecture.go` (add aspect to valid list)
- Modify: `internal/tools/tools.go` (update description)

**Step 1: Add NixInputInfo struct and query**

In `internal/store/architecture.go`, add:

```go
// NixInputInfo describes a Nix flake input.
type NixInputInfo struct {
	Name       string   `json:"name"`
	SourceType string   `json:"source_type"`
	Owner      string   `json:"owner,omitempty"`
	Repo       string   `json:"repo,omitempty"`
	Rev        string   `json:"rev,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
}
```

Add `NixInputs []NixInputInfo` to `ArchitectureInfo`.

In the `GetArchitecture` method, add the `nix_inputs` aspect query:

```go
if wantAspect("nix_inputs", aspects) {
	nodes, _ := s.FindNodesByLabelAndProperty(project, "InfraFile", "infra_type", "nix-input")
	for _, n := range nodes {
		info := NixInputInfo{
			Name:       fmt.Sprintf("%v", n.Properties["input_name"]),
			SourceType: fmt.Sprintf("%v", n.Properties["source_type"]),
		}
		if v, ok := n.Properties["owner"].(string); ok { info.Owner = v }
		if v, ok := n.Properties["repo"].(string); ok { info.Repo = v }
		if v, ok := n.Properties["rev"].(string); ok { info.Rev = v }
		if deps, ok := n.Properties["depends_on"].([]any); ok {
			for _, d := range deps {
				if s, ok := d.(string); ok {
					info.DependsOn = append(info.DependsOn, s)
				}
			}
		}
		result.NixInputs = append(result.NixInputs, info)
	}
}
```

Note: `FindNodesByLabelAndProperty` may not exist. Implement it:

```go
// In internal/store/nodes.go:
func (s *Store) FindNodesByLabelAndProperty(project, label, propKey, propValue string) ([]*Node, error) {
	rows, err := s.q.Query(`SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
		FROM nodes WHERE project=? AND label=? AND json_extract(properties, '$.' || ?) = ?`,
		project, label, propKey, propValue)
	if err != nil {
		return nil, fmt.Errorf("find nodes by label+property: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}
```

**Step 2: Register the aspect**

In `internal/tools/architecture.go`, add `"nix_inputs": true` to `validArchAspects`.

**Step 3: Commit**

```bash
git add internal/store/architecture.go internal/store/nodes.go internal/tools/architecture.go
git commit -m "feat: add nix_inputs architecture aspect

get_architecture(aspects=['nix_inputs']) returns Nix flake inputs
with source metadata. Useful for supply chain visibility on
NixOS-based projects."
```

---

## Feature 3: Security-Aware Node Tagging

### Task 4: Security Role Enrichment Pass

**Finding:** The graph has structural data (calls, imports, types) but no security semantics. A post-flush enrichment pass can pattern-match nodes and assign `security_role` properties based on function names, decorators, file paths, and language conventions.

**Files:**
- Create: `internal/pipeline/security_tags.go`
- Create: `internal/pipeline/security_tags_test.go`

**Step 1: Write the failing test**

Create `internal/pipeline/security_tags_test.go`:

```go
package pipeline

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

func TestClassifySecurityRole(t *testing.T) {
	tests := []struct {
		name     string
		node     *store.Node
		wantRole string
	}{
		{
			name:     "auth middleware by name",
			node:     &store.Node{Name: "requireAuth", Label: "Function", FilePath: "middleware/auth.go"},
			wantRole: "auth_boundary",
		},
		{
			name:     "auth decorator",
			node:     &store.Node{Name: "getUser", Label: "Function", Properties: map[string]any{"decorators": []any{"@login_required"}}},
			wantRole: "auth_boundary",
		},
		{
			name:     "HTTP handler by decorator",
			node:     &store.Node{Name: "createOrder", Label: "Function", Properties: map[string]any{"decorators": []any{"@app.post"}}},
			wantRole: "input_entry_point",
		},
		{
			name:     "route handler by label",
			node:     &store.Node{Name: "/api/orders", Label: "Route"},
			wantRole: "input_entry_point",
		},
		{
			name:     "main function",
			node:     &store.Node{Name: "main", Label: "Function", FilePath: "cmd/server/main.go"},
			wantRole: "input_entry_point",
		},
		{
			name:     "database write function",
			node:     &store.Node{Name: "executeQuery", Label: "Function", FilePath: "db/queries.go"},
			wantRole: "sensitive_sink",
		},
		{
			name:     "file write function",
			node:     &store.Node{Name: "writeConfig", Label: "Function", Properties: map[string]any{"calls_functions": []any{"os.WriteFile"}}},
			wantRole: "sensitive_sink",
		},
		{
			name:     "crypto function",
			node:     &store.Node{Name: "encryptPayload", Label: "Function", FilePath: "crypto/aes.rs"},
			wantRole: "crypto_operation",
		},
		{
			name:     "ordinary function",
			node:     &store.Node{Name: "formatDate", Label: "Function", FilePath: "util/time.go"},
			wantRole: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.node.Properties == nil {
				tt.node.Properties = map[string]any{}
			}
			got := classifySecurityRole(tt.node)
			if got != tt.wantRole {
				t.Errorf("classifySecurityRole(%s) = %q, want %q", tt.node.Name, got, tt.wantRole)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/pipeline/ -run TestClassifySecurityRole -v`
Expected: FAIL (function doesn't exist)

**Step 3: Implement the classifier**

Create `internal/pipeline/security_tags.go`:

```go
package pipeline

import (
	"log/slog"
	"regexp"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// Security role constants.
const (
	RoleAuthBoundary    = "auth_boundary"
	RoleInputEntryPoint = "input_entry_point"
	RoleSensitiveSink   = "sensitive_sink"
	RoleCryptoOperation = "crypto_operation"
)

// authPatterns match function/method names associated with authentication.
var authNamePatterns = regexp.MustCompile(`(?i)(require_?auth|check_?auth|verify_?token|authenticate|authorize|is_?authenticated|validate_?session|check_?permission|require_?login)`)

// authDecoratorPatterns match decorator strings associated with authentication.
var authDecoratorPatterns = regexp.MustCompile(`(?i)(login_required|requires_auth|authenticated|authorize|permission_required|auth_required|protect|guard)`)

// authFilePatterns match file paths in auth-related directories.
var authFilePatterns = regexp.MustCompile(`(?i)(middleware[/\\]auth|auth[/\\]middleware|guards?[/\\]|policies[/\\])`)

// entryPointDecorators match HTTP handler decorators.
var entryPointDecorators = regexp.MustCompile(`(?i)(@app\.(get|post|put|delete|patch)|@router\.|@api_view|@(Get|Post|Put|Delete|Patch)Mapping|#\[axum::)`)

// sinkNamePatterns match function names associated with sensitive operations.
var sinkNamePatterns = regexp.MustCompile(`(?i)(execute_?query|exec_?sql|raw_?query|run_?command|subprocess|os\.exec|write_?file|remove_?file|send_?email|send_?request)`)

// sinkFilePatterns match file paths in database/IO directories.
var sinkFilePatterns = regexp.MustCompile(`(?i)(db[/\\]|database[/\\]|repository[/\\]|repositories[/\\]|queries[/\\]|dal[/\\])`)

// cryptoPatterns match function names or file paths related to cryptography.
var cryptoPatterns = regexp.MustCompile(`(?i)(encrypt|decrypt|hash_?password|sign_?token|verify_?signature|hmac|aes|rsa|pbkdf|argon|bcrypt|scrypt)`)

// cryptoFilePatterns match file paths in crypto directories.
var cryptoFilePatterns = regexp.MustCompile(`(?i)(crypto[/\\]|encryption[/\\]|certs?[/\\]|tls[/\\]|pki[/\\])`)

// classifySecurityRole determines the security role for a node based on its
// name, decorators, file path, and label. Returns empty string if no role matches.
func classifySecurityRole(n *store.Node) string {
	name := n.Name
	filePath := n.FilePath
	label := n.Label
	decorators := nodeDecorators(n)

	// Route nodes are always input entry points
	if label == "Route" {
		return RoleInputEntryPoint
	}

	// Check auth patterns (highest priority for security analysis)
	if authNamePatterns.MatchString(name) || authFilePatterns.MatchString(filePath) {
		return RoleAuthBoundary
	}
	for _, dec := range decorators {
		if authDecoratorPatterns.MatchString(dec) {
			return RoleAuthBoundary
		}
	}

	// Check entry points
	if name == "main" || name == "Main" {
		return RoleInputEntryPoint
	}
	for _, dec := range decorators {
		if entryPointDecorators.MatchString(dec) {
			return RoleInputEntryPoint
		}
	}

	// Check crypto operations
	if cryptoPatterns.MatchString(name) || cryptoFilePatterns.MatchString(filePath) {
		return RoleCryptoOperation
	}

	// Check sensitive sinks
	if sinkNamePatterns.MatchString(name) || sinkFilePatterns.MatchString(filePath) {
		return RoleSensitiveSink
	}

	return ""
}

// nodeDecorators extracts decorator strings from a node's properties.
func nodeDecorators(n *store.Node) []string {
	if n.Properties == nil {
		return nil
	}
	raw, ok := n.Properties["decorators"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// passSecurityTags enriches Function/Method/Class/Route nodes with security_role
// properties based on pattern matching. Runs as a post-flush pass since it needs
// to read all nodes from the DB.
func (p *Pipeline) passSecurityTags() {
	labels := []string{"Function", "Method", "Class", "Route"}
	tagged := 0

	for _, label := range labels {
		nodes, err := p.Store.FindNodesByLabel(p.ProjectName, label)
		if err != nil {
			continue
		}
		for _, n := range nodes {
			role := classifySecurityRole(n)
			if role == "" {
				continue
			}
			// Add security_role to properties and update
			if n.Properties == nil {
				n.Properties = map[string]any{}
			}
			n.Properties["security_role"] = role
			_, _ = p.Store.UpsertNode(n)
			tagged++
		}
	}

	if tagged > 0 {
		slog.Info("pass.security_tags", "tagged", tagged)
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/pipeline/ -run TestClassifySecurityRole -v`
Expected: PASS

**Step 5: Wire into the pipeline**

In `internal/pipeline/pipeline.go`, add to `runPostFlushPasses` (after `passGitHistory`):

```go
	t = time.Now()
	p.passSecurityTags()
	slog.Info("pass.timing", "pass", "security_tags", "elapsed", time.Since(t))
```

**Step 6: Commit**

```bash
git add internal/pipeline/security_tags.go internal/pipeline/security_tags_test.go internal/pipeline/pipeline.go
git commit -m "feat: add security role enrichment pass

Post-flush pass that pattern-matches Function/Method/Class/Route nodes
and assigns security_role properties:
- auth_boundary: auth middleware, permission checks, @login_required
- input_entry_point: HTTP handlers, Route nodes, main functions
- sensitive_sink: DB queries, file writes, subprocess execution
- crypto_operation: encryption, hashing, signing functions

Queryable via: search_graph with property filter or query_graph
WHERE n.security_role = 'auth_boundary'"
```

---

### Task 5: Add security_surfaces Architecture Aspect

**Files:**
- Modify: `internal/store/architecture.go`
- Modify: `internal/tools/architecture.go`

**Step 1: Add SecuritySurface struct and query**

In `internal/store/architecture.go`:

```go
// SecuritySurface summarizes nodes with a specific security role.
type SecuritySurface struct {
	Role       string   `json:"role"`
	Count      int      `json:"count"`
	Examples   []string `json:"examples"`   // up to 5 qualified names
	FilePaths  []string `json:"file_paths"` // unique directories
}
```

Add `SecuritySurfaces []SecuritySurface` to `ArchitectureInfo`.

In `GetArchitecture`, add:

```go
if wantAspect("security_surfaces", aspects) {
	for _, role := range []string{"auth_boundary", "input_entry_point", "sensitive_sink", "crypto_operation"} {
		nodes, _ := s.FindNodesByLabelAndProperty(project, "", "security_role", role)
		// FindNodesByLabelAndProperty with empty label should skip the label filter
		if len(nodes) == 0 {
			continue
		}
		surface := SecuritySurface{Role: role, Count: len(nodes)}
		dirSet := make(map[string]bool)
		for i, n := range nodes {
			if i < 5 {
				surface.Examples = append(surface.Examples, n.QualifiedName)
			}
			dir := filepath.Dir(n.FilePath)
			if dir != "" && dir != "." {
				dirSet[dir] = true
			}
		}
		for d := range dirSet {
			surface.FilePaths = append(surface.FilePaths, d)
		}
		sort.Strings(surface.FilePaths)
		result.SecuritySurfaces = append(result.SecuritySurfaces, surface)
	}
}
```

Update `FindNodesByLabelAndProperty` to handle empty label:

```go
func (s *Store) FindNodesByLabelAndProperty(project, label, propKey, propValue string) ([]*Node, error) {
	query := `SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
		FROM nodes WHERE project=? AND json_extract(properties, '$.' || ?) = ?`
	args := []any{project, propKey, propValue}
	if label != "" {
		query = `SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
			FROM nodes WHERE project=? AND label=? AND json_extract(properties, '$.' || ?) = ?`
		args = []any{project, label, propKey, propValue}
	}
	rows, err := s.q.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("find nodes by property: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}
```

**Step 2: Register the aspect**

In `internal/tools/architecture.go`, add `"security_surfaces": true` to `validArchAspects`.

**Step 3: Commit**

```bash
git add internal/store/architecture.go internal/store/nodes.go internal/tools/architecture.go
git commit -m "feat: add security_surfaces architecture aspect

get_architecture(aspects=['security_surfaces']) returns a summary
of nodes tagged with security roles: counts, example QNs, and
directory distribution per role. Enables quick security posture
overview for any indexed project."
```

---

## Feature 4: STIG Query Tool + Skill Guidance

### Task 6: Add query_security_surfaces MCP Tool

**Finding:** STIG assessments need structured evidence about auth boundaries, input validation, and sensitive operations. A dedicated tool wraps `security_surfaces` data in a STIG-friendly format.

**Files:**
- Modify: `internal/tools/tools.go` (register new tool)
- Create: `internal/tools/security.go` (handler)

**Step 1: Create the handler**

Create `internal/tools/security.go`:

```go
package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerSecurityTools() {
	s.addTool(&mcp.Tool{
		Name:        "query_security_surfaces",
		Description: "Query security-tagged code elements for compliance evidence. Returns functions classified as auth_boundary (authentication/authorization enforcement), input_entry_point (HTTP handlers, CLI entry points), sensitive_sink (database writes, file I/O, subprocess exec), or crypto_operation (encryption, hashing, signing). Use for STIG/compliance evidence gathering: AC-3 → auth_boundary, SI-10 → input_entry_point + sensitive_sink, SC-13 → crypto_operation. Pass role to filter by specific security role, or omit for all. Returns qualified names, file paths, and caller/callee counts for each match.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"role": {
					"type": "string",
					"enum": ["auth_boundary", "input_entry_point", "sensitive_sink", "crypto_operation"],
					"description": "Filter by security role. Omit for all roles."
				},
				"project": {
					"type": "string",
					"description": "Project to query. Defaults to session project."
				},
				"limit": {
					"type": "integer",
					"description": "Max results per role (default 20)"
				}
			}
		}`),
	}, s.handleQuerySecuritySurfaces)
}

func (s *Server) handleQuerySecuritySurfaces(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	st, err := s.resolveStore(getStringArg(args, "project"))
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	projName := s.resolveProjectName(getStringArg(args, "project"))
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	roleFilter := getStringArg(args, "role")
	limit := getIntArg(args, "limit", 20)

	roles := []string{"auth_boundary", "input_entry_point", "sensitive_sink", "crypto_operation"}
	if roleFilter != "" {
		roles = []string{roleFilter}
	}

	type surfaceEntry struct {
		Name          string `json:"name"`
		QualifiedName string `json:"qualified_name"`
		Label         string `json:"label"`
		FilePath      string `json:"file_path"`
		SecurityRole  string `json:"security_role"`
		Callers       int    `json:"callers"`
		Callees       int    `json:"callees"`
	}

	results := make(map[string][]surfaceEntry)
	totalCount := 0

	for _, role := range roles {
		nodes, findErr := st.FindNodesByLabelAndProperty(projName, "", "security_role", role)
		if findErr != nil {
			continue
		}
		entries := make([]surfaceEntry, 0, min(len(nodes), limit))
		for i, n := range nodes {
			if i >= limit {
				break
			}
			callers, callees := st.NodeDegree(n.ID)
			entries = append(entries, surfaceEntry{
				Name:          n.Name,
				QualifiedName: n.QualifiedName,
				Label:         n.Label,
				FilePath:      n.FilePath,
				SecurityRole:  role,
				Callers:       callers,
				Callees:       callees,
			})
		}
		if len(entries) > 0 {
			results[role] = entries
			totalCount += len(nodes)
		}
	}

	responseData := map[string]any{
		"surfaces":    results,
		"total_count": totalCount,
		"stig_hints": map[string]string{
			"AC-3":  "Check auth_boundary nodes enforce access control on all input_entry_point paths",
			"SI-10": "Verify input_entry_point nodes validate input before reaching sensitive_sink nodes",
			"SC-13": "Confirm crypto_operation nodes use FIPS-approved algorithms",
		},
	}

	return jsonResult(responseData), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

Add `"encoding/json"` to the imports.

**Step 2: Register the tool**

In `internal/tools/tools.go`, in the `registerTools` method, add:

```go
s.registerSecurityTools()
```

**Step 3: Commit**

```bash
git add internal/tools/security.go internal/tools/tools.go
git commit -m "feat: add query_security_surfaces MCP tool

Dedicated tool for querying security-tagged nodes with STIG hints.
Maps security roles to STIG controls: auth_boundary -> AC-3,
input_entry_point + sensitive_sink -> SI-10, crypto_operation -> SC-13.
Returns nodes with caller/callee counts for impact assessment."
```

---

### Task 7: STIG Skill Integration Guidance

This task is **outside the codebase-memory-mcp repo**. It documents how to update the `/stig-assess` Claude Code skill to use the new `query_security_surfaces` tool.

**File to modify (external):** `~/.claude/skills/stig-assess/SKILL.md`

**Integration points:**

1. When assessing code-related STIG findings (AC-3, SI-10, SC-13, IA-5), the skill should call `query_security_surfaces` first to gather evidence.

2. Example skill addition:
```markdown
## Code Evidence Gathering (when codebase-memory-mcp is indexed)

For code-related controls, auto-query the code graph before manual review:

- **AC-3 (Access Enforcement):** `query_security_surfaces(role='auth_boundary')` - list all auth checks, then `trace_call_path` from each `input_entry_point` to verify auth coverage
- **SI-10 (Information Input Validation):** `query_security_surfaces(role='input_entry_point')` + `trace_call_path(direction='outbound')` to verify validation exists before `sensitive_sink` nodes
- **SC-13 (Cryptographic Protection):** `query_security_surfaces(role='crypto_operation')` - verify FIPS-approved algorithms, check for hardcoded keys
- **IA-5 (Authenticator Management):** Search for password/token handling: `search_code(pattern='password|token|secret|credential', regex=true)`
```

3. The skill should check if the project is indexed first (`index_status`) and skip graph queries if not.

**No commit needed** - this is a guidance note for a separate repo.

---

### Task 8: Final Verification

**Step 1: Run the full test suite**

Run: `go test ./...`
Expected: All PASS

**Step 2: Re-index Corsair and verify**

```bash
codebase-memory-mcp cli index_repository '{"repo_path": "C:/Users/user/Documents/GHES/psm"}'
codebase-memory-mcp cli get_architecture '{"aspects": ["nix_inputs", "security_surfaces"]}'
codebase-memory-mcp cli query_security_surfaces '{}'
```

**Step 3: Push and create PR**

```bash
git checkout -b feat/corsair-enhancements
git push -u origin feat/corsair-enhancements
gh pr create --title "Corsair enhancements: Nix graph, security tagging, STIG tool" --body "..."
```
