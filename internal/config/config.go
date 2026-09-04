// Package config is the single inventory of environment variables the
// code-graph binary reads.
//
// Every key the product honours is declared here once, with its default and a
// one-line description. Call sites read through Get/IsSet/Truthy so the name,
// the parsing convention, and the documentation stay together; a test in this
// package fails when a key is undocumented in README.md or CLAUDE.md, and
// another fails when production code reads a product key with os.Getenv
// directly.
//
// Values are read live from the process environment on each call. That keeps
// the existing behaviour (tests flip variables with t.Setenv, and the
// embeddings mode is decided once at startup by mutating the environment) while
// still giving the process one place to log the resolved configuration.
package config

import (
	"log/slog"
	"os"
	"sort"
	"strings"
)

// Key describes one environment variable.
type Key struct {
	// Name is the exact environment variable name.
	Name string
	// Default describes the effective value when the variable is unset. It is
	// documentation, not a value that Get returns.
	Default string
	// Doc is a one-line description used by Snapshot and the docs test.
	Doc string
	// Secret marks credentials whose values must never be logged.
	Secret bool
}

// Cloud providers.
var (
	VoyageAPIKey = Key{Name: "VOYAGE_API_KEY", Default: "unset", Doc: "Enables Voyage embeddings; without it code-graph runs offline", Secret: true}
	VoyageModel  = Key{Name: "VOYAGE_EMBED_MODEL", Default: "built-in", Doc: "Voyage embedding model id"}

	AnthropicAPIKey = Key{Name: "ANTHROPIC_API_KEY", Default: "unset", Doc: "Used only by code_localize_agent and rationale tools", Secret: true}
	AnthropicModel  = Key{Name: "ANTHROPIC_MODEL", Default: "built-in", Doc: "Anthropic model id for the localization agent"}
)

// Runtime and storage.
var (
	SkipEmbeddings       = Key{Name: "CODE_GRAPH_SKIP_EMBEDDINGS", Default: "auto", Doc: "1 forces embedding passes off, 0 forces them on; auto follows VOYAGE_API_KEY"}
	EmbeddingsTimeoutSec = Key{Name: "CODE_GRAPH_EMBEDDINGS_TIMEOUT_SEC", Default: "built-in", Doc: "Overall timeout for the embeddings pass"}
	CacheDir             = Key{Name: "CODE_GRAPH_CACHE_DIR", Default: "~/.cache/code-graph", Doc: "Directory holding per-project SQLite databases"}
	LogFile              = Key{Name: "CODE_GRAPH_LOG_FILE", Default: "unset", Doc: "Tee structured logs to this file"}
	LogFileOnly          = Key{Name: "CODE_GRAPH_LOG_FILE_ONLY", Default: "unset", Doc: "Redirect structured logs to this file instead of stderr"}
	HeapLimitMB          = Key{Name: "CODE_GRAPH_HEAP_LIMIT_MB", Default: "off", Doc: "Abort indexing when Go heap-in-use exceeds this many MB"}
	FullReindexEvery     = Key{Name: "CODE_GRAPH_FULL_REINDEX_EVERY", Default: "50", Doc: "Force a full reindex after this many incremental runs; 0 disables"}
	IncrementalCap       = Key{Name: "CODE_GRAPH_INCREMENTAL_CAP", Default: "10000", Doc: "Cap on dependent-set expansion during incremental indexing"}
	AutoRecovery         = Key{Name: "CODE_GRAPH_AUTO_RECOVERY", Default: "unset", Doc: "Set to enable automatic recovery of corrupted project databases"}
	MatchTrace           = Key{Name: "CODE_GRAPH_MATCH_TRACE", Default: "off", Doc: "Log HTTP route matches that scored below threshold"}
	ServiceMap           = Key{Name: "CODE_GRAPH_SERVICE_MAP", Default: "~/.config/code-graph/service_map.json", Doc: "JSON domain-to-pattern table used by service_map and diff_services"}
	NixServiceOptionPfx  = Key{Name: "CODE_GRAPH_NIX_SERVICE_OPTION_PREFIX", Default: "services", Doc: "Option-set prefix for Nix service extraction"}
	Toolset              = Key{Name: "CODE_GRAPH_TOOLSET", Default: "core", Doc: "Which tools the MCP server advertises: core (the plugin, benchmark, and everyday set) or full (every registered tool)"}
	NixPkgsPrefix        = Key{Name: "CODE_GRAPH_NIX_PKGS_PREFIX", Default: "pkgs", Doc: "Package-set prefix for Nix RUNS_BINARY detection"}
)

// Semantic similarity edges (embedding-backed, opt-in).
var (
	SimilarityEdges    = Key{Name: "ENABLE_SIMILARITY_EDGES", Default: "off", Doc: "1/true/yes/on enables SEMANTICALLY_SIMILAR_TO edges after embeddings"}
	SimilarityThresh   = Key{Name: "CODE_GRAPH_SIMILARITY_THRESHOLD", Default: "built-in", Doc: "Minimum cosine similarity for a similarity edge (0-1]"}
	SimilarityTopK     = Key{Name: "CODE_GRAPH_SIMILARITY_TOPK", Default: "built-in", Doc: "Neighbours considered per node for similarity edges"}
	SimilaritySkipHops = Key{Name: "CODE_GRAPH_SIMILARITY_SKIP_HOPS", Default: "built-in", Doc: "Skip pairs already within this many structural hops"}
	SeedMinCosine      = Key{Name: "EMBEDDING_SEED_MIN_COSINE", Default: "0.0", Doc: "Minimum cosine similarity for embedding seeds in ranking"}
)

// Extraction and resolution.
var (
	MaxFileBytes         = Key{Name: "CBM_MAX_FILE_BYTES", Default: "1048576", Doc: "Per-file size cutoff for discovery; 0 disables"}
	SCIPIndexPath        = Key{Name: "CBM_SCIP_INDEX_PATH", Default: "unset", Doc: "Legacy: path to a SCIP index to ingest"}
	SCIPAutoDiscover     = Key{Name: "CBM_SCIP_AUTO_DISCOVER", Default: "off", Doc: "Legacy: auto-discover a SCIP index in the repository"}
	GrammarBaselinesPath = Key{Name: "CBM_GRAMMAR_BASELINES_PATH", Default: "embedded", Doc: "Override the grammar version baseline file used by index_health"}

	ResolverTier2Debug          = Key{Name: "RESOLVER_TIER2_DEBUG", Default: "off", Doc: "Log one line per tier-2 resolver decision"}
	ResolverStaticDispatchDebug = Key{Name: "RESOLVER_STATIC_DISPATCH_DEBUG", Default: "off", Doc: "Log static-dispatch resolution decisions (Rust)"}
	ResolverDropFuzzyJanusian   = Key{Name: "RESOLVER_DROP_FUZZY_JANUSIAN_CHAINS", Default: "python-only", Doc: "1/true/yes forces dropping fuzzy chain resolutions for all languages; 0/false/no disables"}
	ResolverDropLooseCrossPkg   = Key{Name: "RESOLVER_DROP_LOOSE_CROSS_PACKAGE", Default: "emit", Doc: "Drop loose cross-package resolutions instead of emitting them"}
	ResolverRequireImports      = Key{Name: "RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", Default: "language-specific", Doc: "Require an import binding before emitting loose cross-package edges"}
	ResolverEnumVariantParent   = Key{Name: "RESOLVER_EMIT_ENUM_VARIANT_AS_PARENT", Default: "off", Doc: "Resolve unknown enum variants to the enum node"}
)

// Localization agent.
var (
	LocAgentIterations    = Key{Name: "LOCAGENT_ITERATIONS", Default: "2", Doc: "Agent iterations aggregated by MRR (1-3)"}
	LocAgentParallel      = Key{Name: "LOCAGENT_PARALLEL", Default: "off", Doc: "1 runs iterations concurrently"}
	LocAgentPromptVariant = Key{Name: "LOCAGENT_PROMPT_VARIANT", Default: "open", Doc: "open or aggressive system prompt"}
	LocAgentMaxTurns      = Key{Name: "LOCAGENT_MAX_TURNS", Default: "20", Doc: "Hard cap on agent turns"}
	LocAgentRewrite       = Key{Name: "LOCAGENT_REWRITE", Default: "off", Doc: "1 enables the issue-rewrite pre-step"}
	LocAgentBFSDepth      = Key{Name: "LOCAGENT_BFS_DEPTH", Default: "4", Doc: "BFS depth for code_localize inside the agent loop"}
	LocAgentEpisodic      = Key{Name: "LOCAGENT_EPISODIC_MEMORY", Default: "off", Doc: "1 enables episodic-memory retrieval before localization"}
	LocAgentEpisodicProj  = Key{Name: "LOCAGENT_EPISODIC_PROJECT", Default: "built-in", Doc: "Project holding episodic memory"}
	LocAgentEpisodicTopK  = Key{Name: "LOCAGENT_EPISODIC_TOP_K", Default: "built-in", Doc: "Episodic hits retrieved per issue"}
)

// All returns every declared key, sorted by name.
func All() []Key {
	keys := []Key{
		VoyageAPIKey, VoyageModel, AnthropicAPIKey, AnthropicModel,
		SkipEmbeddings, EmbeddingsTimeoutSec, CacheDir, LogFile, LogFileOnly,
		HeapLimitMB, FullReindexEvery, IncrementalCap, AutoRecovery, MatchTrace,
		ServiceMap, NixServiceOptionPfx, NixPkgsPrefix, Toolset,
		SimilarityEdges, SimilarityThresh, SimilarityTopK, SimilaritySkipHops, SeedMinCosine,
		MaxFileBytes, SCIPIndexPath, SCIPAutoDiscover, GrammarBaselinesPath,
		ResolverTier2Debug, ResolverStaticDispatchDebug, ResolverDropFuzzyJanusian,
		ResolverDropLooseCrossPkg, ResolverRequireImports, ResolverEnumVariantParent,
		LocAgentIterations, LocAgentParallel, LocAgentPromptVariant, LocAgentMaxTurns,
		LocAgentRewrite, LocAgentBFSDepth, LocAgentEpisodic, LocAgentEpisodicProj, LocAgentEpisodicTopK,
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	return keys
}

// Get returns the raw value of k from the process environment ("" when unset).
func Get(k Key) string { return os.Getenv(k.Name) }

// IsSet reports whether k is present with a non-empty value.
func IsSet(k Key) bool { return os.Getenv(k.Name) != "" }

// Truthy reports whether k is set to one of 1, true, yes, or on
// (case-insensitive, surrounding whitespace ignored).
func Truthy(k Key) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k.Name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Resolved is one key with its current value, redacted when secret.
type Resolved struct {
	Name  string
	Value string
	Set   bool
}

// Snapshot returns the current value of every key, redacting secrets to
// "<set>". Unset keys report Set=false and Value="".
func Snapshot() []Resolved {
	all := All()
	out := make([]Resolved, 0, len(all))
	for _, k := range all {
		v, ok := os.LookupEnv(k.Name)
		if ok && k.Secret && v != "" {
			v = "<set>"
		}
		out = append(out, Resolved{Name: k.Name, Value: v, Set: ok && v != ""})
	}
	return out
}

// LogResolved emits one debug line per key that is set, so a support log
// shows the effective configuration without exposing secrets.
func LogResolved(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	for _, r := range Snapshot() {
		if r.Set {
			logger.Debug("config.resolved", "key", r.Name, "value", r.Value)
		}
	}
}
