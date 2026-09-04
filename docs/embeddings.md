# Embedding providers

Embeddings power `search_code_semantic`, `find_similar_functions`,
embedding-seeded ranking (`rank_by_query`, `code_localize` with
`seed_strategy=hybrid`), optional `SEMANTICALLY_SIMILAR_TO` edges, and
episodic memory for the localization agent. Everything else in code-graph
works with embeddings off, which is the default when no provider is
configured.

Two provider types are built in, selected by `CODE_GRAPH_EMBED_PROVIDER`:

| Provider | Selected when | Endpoint | Credential |
|---|---|---|---|
| `voyage` | `VOYAGE_API_KEY` is set (or provider forced) | Voyage AI `/v1/embeddings`, asymmetric `input_type` | `VOYAGE_API_KEY` |
| `openai` | `CODE_GRAPH_EMBED_BASE_URL` is set (or provider forced) | `POST {base}/embeddings`, OpenAI wire format | `CODE_GRAPH_EMBED_API_KEY`, else `OPENAI_API_KEY`, else none |
| `off` | neither, or `CODE_GRAPH_EMBED_PROVIDER=off` | none | none |

`auto` (the default) prefers Voyage when its key is present, then an
OpenAI-compatible base URL. Set the provider explicitly to override, for
example `CODE_GRAPH_EMBED_PROVIDER=openai` when both are configured.
`CODE_GRAPH_SKIP_EMBEDDINGS=1` disables the passes regardless of provider.

The startup line names the resolution, for example
`code-graph: embeddings: openai (nomic-embed-text @ localhost:11434)`, and
`code-graph doctor` shows provider, model, endpoint host, and a reachability
probe. Every embedding row stores the model id that produced it, so an index
built with one model and queried with another is detectable.

## Variables

| Variable | Default | Meaning |
|---|---|---|
| `CODE_GRAPH_EMBED_PROVIDER` | `auto` | `auto`, `voyage`, `openai`, or `off` |
| `CODE_GRAPH_EMBED_BASE_URL` | `https://api.openai.com/v1` when `openai` | Base URL; `/embeddings` is appended. Trailing slash is fine. |
| `CODE_GRAPH_EMBED_MODEL` | unset | Required for `openai`. Sent verbatim as `model`. |
| `CODE_GRAPH_EMBED_API_KEY` | `OPENAI_API_KEY` | Sent as `Authorization: Bearer` (default) or `api-key`. Omit for local servers. |
| `CODE_GRAPH_EMBED_AUTH_HEADER` | `bearer` | `bearer` or `api-key` (Azure OpenAI) |
| `CODE_GRAPH_EMBED_DIMENSION` | unset | Expected vector width. A vector of another width fails the batch with an error naming this variable. Unset accepts the API's width but still rejects mixed widths within a batch. |
| `VOYAGE_API_KEY`, `VOYAGE_EMBED_MODEL` | unset, built-in | Voyage provider |

Retries: 429 backs off in 15-second steps, 5xx and transport errors double
from one second, four attempts total, all cancellable through the tool's
context. Batches carry at most 64 texts.

## Vendor settings

Only the Voyage path and a fake OpenAI-compatible server are exercised in this
repository's tests. The rows marked "expected" follow each vendor's published
OpenAI-compatibility documentation and have not been run against the live
service here.

| Vendor | `CODE_GRAPH_EMBED_BASE_URL` | Model example | Notes | Status |
|---|---|---|---|---|
| OpenAI | `https://api.openai.com/v1` (default) | `text-embedding-3-small` (1536), `text-embedding-3-large` (3072) | `OPENAI_API_KEY` works as the credential | expected |
| Azure OpenAI | `https://<resource>.openai.azure.com/openai/v1` | your deployment name as the model | Set `CODE_GRAPH_EMBED_AUTH_HEADER=api-key`. The v1 surface accepts `Authorization: Bearer` too on recent API versions; if you use a deployment-scoped URL with `?api-version=`, use a gateway instead, because the client appends `/embeddings` and sends no query string | expected |
| Google Gemini | `https://generativelanguage.googleapis.com/v1beta/openai` | `gemini-embedding-001` | Gemini API key as Bearer; set `CODE_GRAPH_EMBED_DIMENSION` to the width you configure on the model | expected |
| Amazon Bedrock | your gateway URL (LiteLLM proxy, Bedrock Access Gateway) | e.g. `amazon.titan-embed-text-v2:0` | Bedrock has no OpenAI-compatible endpoint of its own; a gateway translates | expected |
| Ollama | `http://localhost:11434/v1` | `nomic-embed-text` (768), `mxbai-embed-large` (1024) | No key needed; `ollama pull <model>` first | fake-server tested, live expected |
| vLLM | `http://<host>:8000/v1` | the served model name | Key only if you started vLLM with `--api-key` | expected |
| LM Studio | `http://localhost:1234/v1` | the loaded embedding model id | No key by default | expected |
| OpenRouter | `https://openrouter.ai/api/v1` | provider-prefixed ids such as `openai/text-embedding-3-small` | OpenRouter key as Bearer | expected |

Example, fully local:

```bash
export CODE_GRAPH_EMBED_BASE_URL=http://localhost:11434/v1
export CODE_GRAPH_EMBED_MODEL=nomic-embed-text
export CODE_GRAPH_EMBED_DIMENSION=768
code-graph doctor          # provider: openai, endpoint localhost:11434
```

Switching models or providers on an existing project does not migrate stored
vectors. Re-run `index_repository(force=true)` so every row is produced by the
same model; `doctor` and `index_health` surface the model recorded on disk.

## What is sent

With a provider configured, code-graph sends the text it embeds: node names,
qualified names, signatures, and short surrounding context for functions,
methods, types, and modules, plus the query text of semantic searches. It does
not send whole files. With `voyage` that goes to Voyage AI; with `openai` it
goes to whatever host `CODE_GRAPH_EMBED_BASE_URL` names, which may be your own
machine. With no provider nothing leaves the host. See `SECURITY.md`.
