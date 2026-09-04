package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/brandyn-s/code-graph/internal/config"
	"github.com/brandyn-s/code-graph/internal/embed"
)

// embeddingMode decides whether the embedding passes run for this process and
// returns a one-line human-readable status for stderr.
//
// Rules:
//   - CODE_GRAPH_SKIP_EMBEDDINGS=1/true always disables embeddings.
//   - CODE_GRAPH_SKIP_EMBEDDINGS=0/false always enables them (the pipeline
//     still needs a configured provider and reports a missing one itself).
//   - Otherwise embeddings are enabled only when a provider resolves: Voyage
//     when VOYAGE_API_KEY is set, or an OpenAI-compatible endpoint when
//     CODE_GRAPH_EMBED_BASE_URL (and CODE_GRAPH_EMBED_MODEL) are set. A fresh
//     install with neither works fully offline: graph indexing, tracing and
//     evidence never need a network call.
func embeddingMode(getenv func(string) string) (enabled bool, status string) {
	skip := strings.TrimSpace(getenv(config.SkipEmbeddings.Name))
	res := embed.ResolveProvider(getenv)
	configured := res.Provider != embed.ProviderOff
	switch {
	case skip == "1" || strings.EqualFold(skip, "true"):
		return false, "code-graph: embeddings disabled (CODE_GRAPH_SKIP_EMBEDDINGS set)"
	case skip == "0" || strings.EqualFold(skip, "false"):
		if configured {
			return true, "code-graph: embeddings: " + res.Describe()
		}
		return true, "code-graph: embeddings requested but " + res.Reason + "; embedding passes will report the missing provider"
	case configured:
		return true, "code-graph: embeddings: " + res.Describe()
	case res.Err != nil || res.Requested != embed.ProviderAuto:
		// Misconfiguration, or an explicit off: say exactly why.
		return false, "code-graph: embeddings disabled (" + res.Reason + ")"
	default:
		return false, "code-graph: embeddings disabled (" + embed.NoProviderHint + " to enable semantic node search)"
	}
}

// applyEmbeddingMode logs the effective embedding mode and, when embeddings
// are disabled by default (no provider), makes that explicit for every
// downstream pass by setting CODE_GRAPH_SKIP_EMBEDDINGS=1 in the process
// environment.
func applyEmbeddingMode() {
	enabled, status := embeddingMode(os.Getenv)
	if !enabled && config.Get(config.SkipEmbeddings) == "" {
		_ = os.Setenv(config.SkipEmbeddings.Name, "1")
	}
	fmt.Fprintln(os.Stderr, status)
}
