package pipeline

import "github.com/brandyn-s/code-graph/internal/embed"

// NewVoyageClient returns the configured embedding provider, or nil when
// embeddings are disabled (no VOYAGE_API_KEY). Kept for callers that use the
// nil check as the "embeddings available?" signal; new code should call
// embed.Default and check embed.IsDisabled.
func NewVoyageClient() embed.Embedder {
	e := embed.Default()
	if embed.IsDisabled(e) {
		return nil
	}
	return e
}
