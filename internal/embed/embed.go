// Package embed defines the embedding provider seam used by the indexing
// pipeline, semantic search, ranking seeds, and episodic memory.
//
// Production code depends on the Embedder interface, not on a concrete
// client, so a new provider is one type that satisfies Embedder plus a case
// in FromResolution. When no provider is configured, Default returns
// Disabled, whose methods fail with ErrDisabled; callers log the reason and
// skip embedding work instead of crashing.
//
// Two providers ship: Voyage (native API, asymmetric input types) and any
// OpenAI-compatible embeddings endpoint (OpenAI, Azure OpenAI, Gemini's
// OpenAI surface, Ollama, vLLM, LM Studio, OpenRouter, gateways). Selection
// is described by ResolveProvider so the startup log, doctor, and the
// pipeline all agree on which provider is active and why.
package embed

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/brandyn-s/code-graph/internal/config"
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

// NoProviderHint tells a user how to turn embeddings on.
const NoProviderHint = "set VOYAGE_API_KEY, or CODE_GRAPH_EMBED_BASE_URL and CODE_GRAPH_EMBED_MODEL for an OpenAI-compatible endpoint"

// ErrDisabled is returned by Disabled when embedding work is requested
// without a configured provider.
var ErrDisabled = errors.New("embeddings disabled: no embedding provider configured (" + NoProviderHint + ")")

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

// Provider names accepted by CODE_GRAPH_EMBED_PROVIDER.
const (
	ProviderAuto   = "auto"
	ProviderVoyage = "voyage"
	ProviderOpenAI = "openai"
	ProviderOff    = "off"
)

// DefaultOpenAIBaseURL is used when the openai provider is selected without
// CODE_GRAPH_EMBED_BASE_URL.
const DefaultOpenAIBaseURL = "https://api.openai.com/v1"

// Resolution is the outcome of reading the provider configuration: which
// provider is active, with what model and endpoint, or why none is.
type Resolution struct {
	// Provider is voyage, openai, or off.
	Provider string
	// Requested is the raw CODE_GRAPH_EMBED_PROVIDER value (auto when unset).
	Requested string
	// Model is the model id that will be sent to the provider.
	Model string
	// BaseURL is the OpenAI-compatible endpoint (openai only), no trailing slash.
	BaseURL string
	// Dimension is the expected vector width when CODE_GRAPH_EMBED_DIMENSION
	// is set; 0 means "accept what the API returns".
	Dimension int
	// AuthHeader is bearer or api-key (openai only).
	AuthHeader string
	// HasCredential reports whether a key will be sent.
	HasCredential bool
	// Reason explains an off Provider, including misconfigurations.
	Reason string
	// Err is set when the configuration is contradictory (for example the
	// openai provider without a model); Provider is off in that case.
	Err error
}

// Host returns the endpoint host for logs and doctor output ("" for voyage
// or off).
func (r *Resolution) Host() string {
	if r.BaseURL == "" {
		return ""
	}
	u, err := url.Parse(r.BaseURL)
	if err != nil || u.Host == "" {
		return r.BaseURL
	}
	return u.Host
}

// Describe returns a one-line human summary such as "voyage (voyage-code-3)"
// or "openai (nomic-embed-text @ localhost:11434)".
func (r *Resolution) Describe() string {
	switch r.Provider {
	case ProviderVoyage:
		return fmt.Sprintf("%s (%s)", r.Provider, r.Model)
	case ProviderOpenAI:
		return fmt.Sprintf("%s (%s @ %s)", r.Provider, r.Model, r.Host())
	default:
		return ProviderOff
	}
}

// ResolveProvider reads the provider configuration through getenv (os.Getenv
// in production, a map in tests) and returns the effective Resolution.
//
// Rules, in order:
//   - CODE_GRAPH_EMBED_PROVIDER=off → off.
//   - =voyage → voyage when VOYAGE_API_KEY is set, else off with a reason.
//   - =openai → openai; CODE_GRAPH_EMBED_MODEL is required, the base URL
//     defaults to api.openai.com, the key is CODE_GRAPH_EMBED_API_KEY then
//     OPENAI_API_KEY and may be absent for self-hosted endpoints.
//   - auto (default) → voyage when VOYAGE_API_KEY is set, else openai when
//     CODE_GRAPH_EMBED_BASE_URL is set, else off.
//
// CODE_GRAPH_SKIP_EMBEDDINGS is deliberately not consulted here: it gates
// whether embedding passes run at all and is handled by the caller.
func ResolveProvider(getenv func(string) string) Resolution {
	get := func(k config.Key) string { return strings.TrimSpace(getenv(k.Name)) }
	requested := strings.ToLower(get(config.EmbedProvider))
	if requested == "" {
		requested = ProviderAuto
	}
	r := Resolution{Requested: requested, Provider: ProviderOff}

	voyageKey := get(config.VoyageAPIKey)
	baseURL := get(config.EmbedBaseURL)

	provider := requested
	if requested == ProviderAuto {
		switch {
		case voyageKey != "":
			provider = ProviderVoyage
		case baseURL != "":
			provider = ProviderOpenAI
		default:
			r.Reason = "no embedding provider configured (" + NoProviderHint + ")"
			return r
		}
	}

	// fail returns an off Resolution carrying only the request and the
	// reason, so partially resolved fields never leak into logs or doctor.
	fail := func(reason string) Resolution {
		return Resolution{Requested: requested, Provider: ProviderOff, Reason: reason, Err: errors.New(reason)}
	}

	switch provider {
	case ProviderOff:
		r.Reason = "CODE_GRAPH_EMBED_PROVIDER=off"
		return r
	case ProviderVoyage:
		if voyageKey == "" {
			return fail("CODE_GRAPH_EMBED_PROVIDER=voyage but VOYAGE_API_KEY is unset")
		}
		r.Provider = ProviderVoyage
		r.HasCredential = true
		r.Model = get(config.VoyageModel)
		if r.Model == "" {
			r.Model = voyageModel
		}
		return r
	case ProviderOpenAI:
		r.Model = get(config.EmbedModel)
		if r.Model == "" {
			return fail("openai embedding provider selected but CODE_GRAPH_EMBED_MODEL is unset")
		}
		if baseURL == "" {
			baseURL = DefaultOpenAIBaseURL
		}
		if u, err := url.Parse(baseURL); err != nil || u.Scheme == "" || u.Host == "" {
			return fail(fmt.Sprintf("CODE_GRAPH_EMBED_BASE_URL %q is not an absolute http(s) URL", baseURL))
		}
		r.BaseURL = strings.TrimRight(baseURL, "/")
		key := get(config.EmbedAPIKey)
		if key == "" {
			key = get(config.OpenAIAPIKey)
		}
		r.HasCredential = key != ""
		r.AuthHeader = strings.ToLower(get(config.EmbedAuthHeader))
		if r.AuthHeader == "" {
			r.AuthHeader = AuthBearer
		}
		if r.AuthHeader != AuthBearer && r.AuthHeader != AuthAPIKey {
			return fail(fmt.Sprintf("CODE_GRAPH_EMBED_AUTH_HEADER must be %s or %s, got %q", AuthBearer, AuthAPIKey, r.AuthHeader))
		}
		if dim := get(config.EmbedDimension); dim != "" {
			n, err := strconv.Atoi(dim)
			if err != nil || n <= 0 {
				return fail(fmt.Sprintf("CODE_GRAPH_EMBED_DIMENSION must be a positive integer, got %q", dim))
			}
			r.Dimension = n
		}
		r.Provider = ProviderOpenAI
		return r
	default:
		return fail(fmt.Sprintf("unknown CODE_GRAPH_EMBED_PROVIDER %q (want auto, voyage, openai, or off)", requested))
	}
}

// Default resolves the configured provider from the environment and returns
// a ready client, or Disabled when none is configured or the configuration
// is contradictory (the reason is available through ResolveProvider).
func Default() Embedder {
	res := ResolveProvider(os.Getenv)
	return FromResolution(&res)
}

// FromResolution builds the client described by r. Credentials are read from
// the environment at construction time; r only says which provider to use.
func FromResolution(r *Resolution) Embedder {
	switch r.Provider {
	case ProviderVoyage:
		if v := NewVoyage(); v != nil {
			return v
		}
	case ProviderOpenAI:
		if c := NewOpenAI(r); c != nil {
			return c
		}
	}
	return Disabled{}
}
