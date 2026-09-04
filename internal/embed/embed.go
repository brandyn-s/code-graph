// Package embed defines the embedding provider seam used by the indexing
// pipeline, semantic search, ranking seeds, and episodic memory.
//
// Production code depends on the Embedder interface, not on a concrete
// client, so a new provider is one type that satisfies Embedder plus a case
// in Default. When no provider is configured, Default returns Disabled, whose
// methods fail with ErrDisabled; callers log the reason and skip embedding
// work instead of crashing.
package embed

import (
	"context"
	"errors"
)

// Embedder turns text into fixed-width vectors.
//
// inputType is the provider's asymmetric hint: "document" while indexing and
// "query" while searching. Implementations that do not distinguish the two
// may ignore it.
type Embedder interface {
	// Model returns the model identifier that produced the vectors, so tool
	// responses and stored rows can record real provenance.
	Model() string
	// EmbedBatch embeds texts in order and honours ctx cancellation.
	EmbedBatch(ctx context.Context, texts []string, inputType string) ([][]float32, error)
	// EmbedSingle embeds one text.
	EmbedSingle(ctx context.Context, text string, inputType string) ([]float32, error)
}

// ErrDisabled is returned by Disabled when embedding work is requested
// without a configured provider.
var ErrDisabled = errors.New("embeddings disabled: VOYAGE_API_KEY is not set")

// BatchSize is the largest text batch a provider call should carry; callers
// chunk larger inputs so progress and cancellation stay responsive.
const BatchSize = 64 // Voyage rate limit: 64 texts per batch is safe

// Disabled is the Embedder used when no provider is configured.
type Disabled struct{}

// Model reports an empty model id.
func (Disabled) Model() string { return "" }

// EmbedBatch always fails with ErrDisabled.
func (Disabled) EmbedBatch(context.Context, []string, string) ([][]float32, error) {
	return nil, ErrDisabled
}

// EmbedSingle always fails with ErrDisabled.
func (Disabled) EmbedSingle(context.Context, string, string) ([]float32, error) {
	return nil, ErrDisabled
}

// IsDisabled reports whether e is the Disabled provider (or nil).
func IsDisabled(e Embedder) bool {
	if e == nil {
		return true
	}
	_, ok := e.(Disabled)
	return ok
}

// Default resolves the configured provider from the environment: Voyage when
// VOYAGE_API_KEY is set, otherwise Disabled. Adding a provider means adding a
// branch here and documenting its variables in internal/config.
func Default() Embedder {
	if v := NewVoyage(); v != nil {
		return v
	}
	return Disabled{}
}
