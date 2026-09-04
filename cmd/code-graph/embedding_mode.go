package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/brandyn-s/code-graph/internal/config"
)

// embeddingMode decides whether the Voyage embedding passes run for this
// process and returns a one-line human-readable status for stderr.
//
// Rules:
//   - CODE_GRAPH_SKIP_EMBEDDINGS=1/true always disables embeddings.
//   - CODE_GRAPH_SKIP_EMBEDDINGS=0/false always enables them (the pipeline
//     still needs VOYAGE_API_KEY and will report the missing key itself).
//   - Otherwise embeddings are enabled only when VOYAGE_API_KEY is set. A
//     fresh install with no key therefore works fully offline: graph
//     indexing, tracing and evidence never need a network call.
func embeddingMode(getenv func(string) string) (enabled bool, status string) {
	skip := strings.TrimSpace(getenv(config.SkipEmbeddings.Name))
	hasKey := strings.TrimSpace(getenv(config.VoyageAPIKey.Name)) != ""
	switch {
	case skip == "1" || strings.EqualFold(skip, "true"):
		return false, "code-graph: embeddings disabled (CODE_GRAPH_SKIP_EMBEDDINGS set)"
	case skip == "0" || strings.EqualFold(skip, "false"):
		if hasKey {
			return true, "code-graph: embeddings: voyage"
		}
		return true, "code-graph: embeddings requested but VOYAGE_API_KEY is unset; embedding passes will report the missing key"
	case hasKey:
		return true, "code-graph: embeddings: voyage"
	default:
		return false, "code-graph: embeddings disabled (set VOYAGE_API_KEY to enable semantic node search)"
	}
}

// applyEmbeddingMode logs the effective embedding mode and, when embeddings
// are disabled by default (no key), makes that explicit for every downstream
// pass by setting CODE_GRAPH_SKIP_EMBEDDINGS=1 in the process environment.
func applyEmbeddingMode() {
	enabled, status := embeddingMode(os.Getenv)
	if !enabled && config.Get(config.SkipEmbeddings) == "" {
		_ = os.Setenv(config.SkipEmbeddings.Name, "1")
	}
	fmt.Fprintln(os.Stderr, status)
}
