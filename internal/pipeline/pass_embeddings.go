package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

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
func (p *Pipeline) passEmbeddings() {
	vc := NewVoyageClient()
	if vc == nil {
		slog.Info("pass.embeddings.skip", "reason", "VOYAGE_API_KEY not set")
		return
	}

	// Fetch embeddable nodes
	rows, err := p.Store.DB().QueryContext(context.Background(),
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

	slog.Info("pass.embeddings.start", "nodes", len(nodes))

	// Batch embed
	const batchSize = 128
	embedded := 0
	for i := 0; i < len(nodes); i += batchSize {
		end := i + batchSize
		if end > len(nodes) {
			end = len(nodes)
		}
		batch := nodes[i:end]

		texts := make([]string, len(batch))
		for j, n := range batch {
			texts[j] = n.text
		}

		vecs, err := vc.EmbedBatch(texts, "document")
		if err != nil {
			slog.Warn("pass.embeddings.batch.err", "batch", i, "err", err)
			continue // Skip failed batch, continue with rest
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
