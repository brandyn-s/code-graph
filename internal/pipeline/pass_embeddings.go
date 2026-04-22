package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Default overall timeout for the embeddings pass. Large graphs with many
// embeddable nodes can legitimately take minutes; the cap prevents an
// indefinite stall from blocking the rest of the index pipeline.
// Override with CODE_GRAPH_EMBEDDINGS_TIMEOUT_SEC.
const embeddingsPassDefaultTimeout = 10 * time.Minute

// embeddableLabels are the node types worth embedding for semantic search.
var embeddableLabels = map[string]bool{
	"Function":  true,
	"Method":    true,
	"Class":     true,
	"Struct":    true,
	"Interface": true,
	"Trait":     true,
	"Enum":      true,
	"Module":    true,
	"Type":      true,
}

// passEmbeddings generates Voyage AI embeddings for embeddable nodes.
// Runs after all other enrichment passes so node properties (docstring,
// signature, security_role) are fully populated.
//
// Each node's embedding input is: "# {label} {qualified_name}\n{signature or source}\n{docstring}"
// This gives the embedding model both the identity and the content.
//
// Bounded runtime: the whole pass runs under a context.WithTimeout so a
// misbehaving upstream (slow Voyage API, stalled HTTP connection) can't hang
// the indexer indefinitely. Any batches completed before timeout are
// persisted; the next incremental run resumes the rest.
//
// Env overrides:
//   CODE_GRAPH_SKIP_EMBEDDINGS=1  — skip the pass entirely regardless of
//                                   VOYAGE_API_KEY. Useful for baseline/CI
//                                   runs or when semantic_search is not
//                                   needed.
//   CODE_GRAPH_EMBEDDINGS_TIMEOUT_SEC=<n>
//                                 — override the default 600s overall cap.
func (p *Pipeline) passEmbeddings() {
	if skip := os.Getenv("CODE_GRAPH_SKIP_EMBEDDINGS"); skip == "1" || strings.EqualFold(skip, "true") {
		slog.Info("pass.embeddings.skip", "reason", "CODE_GRAPH_SKIP_EMBEDDINGS set")
		return
	}

	vc := NewVoyageClient()
	if vc == nil {
		slog.Info("pass.embeddings.skip", "reason", "VOYAGE_API_KEY not set")
		return
	}

	// Resolve overall timeout.
	timeout := embeddingsPassDefaultTimeout
	if v := os.Getenv("CODE_GRAPH_EMBEDDINGS_TIMEOUT_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		} else {
			slog.Warn("pass.embeddings.bad_timeout_env",
				"var", "CODE_GRAPH_EMBEDDINGS_TIMEOUT_SEC", "val", v,
				"using", timeout.String())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Fetch embeddable nodes
	rows, err := p.Store.DB().QueryContext(ctx,
		`SELECT id, label, name, qualified_name, file_path, properties
		 FROM nodes
		 WHERE project = ? AND label IN (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ORDER BY id`,
		p.ProjectName,
		"Function", "Method", "Class", "Struct", "Interface",
		"Trait", "Enum", "Module", "Type")
	if err != nil {
		slog.Warn("pass.embeddings.query.err", "err", err)
		return
	}
	defer rows.Close()

	type nodeInfo struct {
		id    int64
		label string
		qname string
		text  string
	}

	var nodes []nodeInfo
	for rows.Next() {
		var id int64
		var label, name, qname, file, propsJSON string
		if err := rows.Scan(&id, &label, &name, &qname, &file, &propsJSON); err != nil {
			slog.Warn("pass.embeddings.scan.err", "err", err)
			continue
		}

		text := buildEmbeddingText(label, qname, propsJSON)
		nodes = append(nodes, nodeInfo{id: id, label: label, qname: qname, text: text})
	}

	if len(nodes) == 0 {
		slog.Info("pass.embeddings.skip", "reason", "no embeddable nodes")
		return
	}

	slog.Info("pass.embeddings.start", "nodes", len(nodes), "timeout", timeout.String())

	// Batch embed.
	//
	// batchSize matches voyageBatchSize so one outer iteration == one API
	// call. This gives per-API-call progress logs (previously progress only
	// fired per 128 nodes = 2 API calls, leaving users with no feedback
	// between the "generating embeddings" start log and the first progress
	// log several minutes later on stalled requests).
	const batchSize = voyageBatchSize
	embedded := 0
	var lastErr error
batchLoop:
	for i := 0; i < len(nodes); i += batchSize {
		// Context gate: if the pass timeout fires or caller cancels, stop
		// cleanly. Already-persisted batches are kept; the next incremental
		// index run resumes from where we left off.
		if err := ctx.Err(); err != nil {
			lastErr = err
			break batchLoop
		}

		end := i + batchSize
		if end > len(nodes) {
			end = len(nodes)
		}
		batch := nodes[i:end]

		texts := make([]string, len(batch))
		for j, n := range batch {
			texts[j] = n.text
		}

		vecs, err := vc.EmbedBatch(ctx, texts, "document")
		if err != nil {
			// Context errors are terminal for this pass — don't try more
			// batches; the network or upstream is plainly not ready.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				lastErr = err
				break batchLoop
			}
			slog.Warn("pass.embeddings.batch.err", "batch", i, "err", err)
			continue // Transient API error on this batch; try the next.
		}

		// Store in DB
		nodeIDs := make([]int64, len(batch))
		for j, n := range batch {
			nodeIDs[j] = n.id
		}

		if err := p.Store.UpsertEmbeddingBatch(nodeIDs, vc.model, vecs); err != nil {
			slog.Warn("pass.embeddings.store.err", "batch", i, "err", err)
			continue
		}

		embedded += len(batch)
		slog.Info("pass.embeddings.progress", "embedded", embedded, "total", len(nodes))
	}

	// Invalidate the in-memory cache so next search loads fresh data
	p.Store.InvalidateEmbeddingCache()

	if lastErr != nil {
		slog.Warn("pass.embeddings.truncated",
			"embedded", embedded, "total", len(nodes), "reason", lastErr,
			"hint", "partial results persisted; next incremental index will resume")
		return
	}
	slog.Info("pass.embeddings.done", "embedded", embedded, "total", len(nodes))
}

// buildEmbeddingText constructs the text to embed for a node.
// Format: "# {label} {qualified_name}\n{signature}\n{docstring}"
func buildEmbeddingText(label, qname, propsJSON string) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("# %s %s", label, qname))

	// Extract signature and docstring from properties JSON
	// Properties is a JSON string like {"signature": "fn foo()", "docstring": "Does X"}
	props := parsePropsJSON(propsJSON)

	if sig, ok := props["signature"]; ok && sig != "" {
		parts = append(parts, sig)
	}

	if doc, ok := props["docstring"]; ok && doc != "" {
		// Truncate long docstrings
		if len(doc) > 500 {
			doc = doc[:500] + "..."
		}
		parts = append(parts, doc)
	}

	// If no signature or docstring, include the source snippet if available
	if len(parts) == 1 {
		if src, ok := props["source_snippet"]; ok && src != "" {
			if len(src) > 500 {
				src = src[:500] + "..."
			}
			parts = append(parts, src)
		}
	}

	return strings.Join(parts, "\n")
}

// parsePropsJSON extracts string values from a JSON properties blob.
func parsePropsJSON(propsJSON string) map[string]string {
	result := make(map[string]string)
	if propsJSON == "" || propsJSON == "{}" {
		return result
	}

	// Simple JSON extraction without full unmarshal for performance
	// Look for "key": "value" patterns
	for _, key := range []string{"signature", "docstring", "source_snippet"} {
		needle := fmt.Sprintf(`"%s":"`, key)
		idx := strings.Index(propsJSON, needle)
		if idx < 0 {
			needle = fmt.Sprintf(`"%s": "`, key) // with space after colon
			idx = strings.Index(propsJSON, needle)
		}
		if idx >= 0 {
			start := idx + len(needle)
			// Find closing quote (handle escaped quotes)
			end := start
			for end < len(propsJSON) {
				if propsJSON[end] == '"' && (end == start || propsJSON[end-1] != '\\') {
					break
				}
				end++
			}
			if end < len(propsJSON) {
				result[key] = strings.ReplaceAll(propsJSON[start:end], `\"`, `"`)
			}
		}
	}
	return result
}
