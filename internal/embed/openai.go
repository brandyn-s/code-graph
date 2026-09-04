package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/brandyn-s/code-graph/internal/config"
)

// Credential header styles for the OpenAI-compatible provider.
const (
	AuthBearer = "bearer"  // Authorization: Bearer <key> (OpenAI, Ollama, vLLM, OpenRouter, Gemini)
	AuthAPIKey = "api-key" // api-key: <key> (Azure OpenAI)
)

const (
	openAIMaxAttempts = 4
	openAIBatchSize   = BatchSize
)

// openAIEmbedRequest is the body for POST {base}/embeddings. OpenAI-compatible
// APIs have no input_type, so the asymmetric hint is dropped.
type openAIEmbedRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

// openAIEmbedResponse is the subset of the response that matters. Index is
// used to restore input order; some servers return data unordered.
type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// OpenAI embeds text through any OpenAI-compatible /embeddings endpoint.
type OpenAI struct {
	baseURL    string
	apiKey     string
	authHeader string
	model      string
	dimension  int
	client     *http.Client

	// retryWait computes the sleep before the next attempt after a retryable
	// failure. Tests replace it so retries run instantly.
	retryWait func(status, attempt int) time.Duration
	// batchDelay separates consecutive batches within one EmbedBatch call.
	// Zero by default: most OpenAI-compatible servers do not need pacing.
	batchDelay time.Duration
}

// Compile-time check that OpenAI satisfies Embedder.
var _ Embedder = (*OpenAI)(nil)

// NewOpenAI builds a client from a Resolution whose Provider is openai.
// Returns nil for any other provider or when the model is empty.
func NewOpenAI(r *Resolution) *OpenAI {
	if r.Provider != ProviderOpenAI || r.Model == "" || r.BaseURL == "" {
		return nil
	}
	key := strings.TrimSpace(config.Get(config.EmbedAPIKey))
	if key == "" {
		key = strings.TrimSpace(config.Get(config.OpenAIAPIKey))
	}
	auth := r.AuthHeader
	if auth == "" {
		auth = AuthBearer
	}
	return &OpenAI{
		baseURL:    strings.TrimRight(r.BaseURL, "/"),
		apiKey:     key,
		authHeader: auth,
		model:      r.Model,
		dimension:  r.Dimension,
		client:     &http.Client{Timeout: 120 * time.Second},
		retryWait:  defaultOpenAIRetryWait,
	}
}

// defaultOpenAIRetryWait mirrors the Voyage policy: rate limits back off in
// 15 s steps, server errors and transport failures double from 1 s.
func defaultOpenAIRetryWait(status, attempt int) time.Duration {
	if status == http.StatusTooManyRequests {
		return 15 * time.Duration(attempt+1) * time.Second
	}
	return time.Duration(1<<attempt) * time.Second
}

// Model returns the model id sent to the endpoint. Stored embedding rows
// record it, so an index mixing providers or models is detectable.
func (c *OpenAI) Model() string { return c.model }

// BaseURL returns the configured endpoint (for doctor and logs).
func (c *OpenAI) BaseURL() string { return c.baseURL }

// EmbedBatch embeds texts in order, in bounded batches, honouring ctx.
func (c *OpenAI) EmbedBatch(ctx context.Context, texts []string, _ string) ([][]float32, error) {
	var all [][]float32
	for i := 0; i < len(texts); i += openAIBatchSize {
		if err := ctx.Err(); err != nil {
			return all, err
		}
		end := i + openAIBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		if i > 0 && c.batchDelay > 0 {
			select {
			case <-ctx.Done():
				return all, ctx.Err()
			case <-time.After(c.batchDelay):
			}
		}
		vecs, err := c.embedOnce(ctx, texts[i:end])
		if err != nil {
			return all, fmt.Errorf("batch %d-%d: %w", i, end, err)
		}
		all = append(all, vecs...)
	}
	return all, nil
}

// EmbedSingle embeds one text.
func (c *OpenAI) EmbedSingle(ctx context.Context, text, _ string) ([]float32, error) {
	vecs, err := c.embedOnce(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("empty response from %s", c.baseURL)
	}
	return vecs[0], nil
}

func (c *OpenAI) embedOnce(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(openAIEmbedRequest{Input: texts, Model: c.model})
	if err != nil {
		return nil, err
	}
	endpoint := c.baseURL + "/embeddings"

	for attempt := 0; attempt < openAIMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		c.setAuth(req)

		resp, err := c.client.Do(req)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			if attempt < openAIMaxAttempts-1 {
				wait := c.retryWait(0, attempt)
				slog.Warn("openai_embed.request.err", "err", err, "attempt", attempt+1, "wait", wait)
				if !sleepCtx(ctx, wait) {
					return nil, ctx.Err()
				}
				continue
			}
			return nil, err
		}
		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read response: %w", readErr)
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			if attempt < openAIMaxAttempts-1 {
				wait := c.retryWait(resp.StatusCode, attempt)
				slog.Warn("openai_embed.api.retry", "status", resp.StatusCode, "attempt", attempt+1, "wait", wait)
				if !sleepCtx(ctx, wait) {
					return nil, ctx.Err()
				}
				continue
			}
			return nil, fmt.Errorf("embeddings API error %d from %s: %s", resp.StatusCode, c.baseURL, truncate(respBody))
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("embeddings API error %d from %s: %s", resp.StatusCode, c.baseURL, truncate(respBody))
		}
		return c.parse(respBody, len(texts))
	}
	return nil, fmt.Errorf("exhausted retries against %s", c.baseURL)
}

func (c *OpenAI) setAuth(req *http.Request) {
	if c.apiKey == "" {
		return
	}
	if c.authHeader == AuthAPIKey {
		req.Header.Set("api-key", c.apiKey)
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
}

// parse decodes the response, restores input order, and enforces a uniform
// vector width (and the configured dimension when set).
func (c *OpenAI) parse(body []byte, want int) ([][]float32, error) {
	var out openAIEmbedResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("embeddings API error from %s: %s", c.baseURL, out.Error.Message)
	}
	if len(out.Data) != want {
		return nil, fmt.Errorf("embeddings API returned %d vectors for %d inputs", len(out.Data), want)
	}
	vecs := make([][]float32, want)
	for _, item := range out.Data {
		if item.Index < 0 || item.Index >= want {
			return nil, fmt.Errorf("embeddings API returned out-of-range index %d", item.Index)
		}
		if vecs[item.Index] != nil {
			return nil, fmt.Errorf("embeddings API returned duplicate index %d", item.Index)
		}
		vecs[item.Index] = item.Embedding
	}
	width := c.dimension
	for i, v := range vecs {
		if len(v) == 0 {
			return nil, fmt.Errorf("embeddings API returned an empty vector at index %d", i)
		}
		if width == 0 {
			width = len(v)
		}
		if len(v) != width {
			if c.dimension > 0 {
				return nil, fmt.Errorf("model %s returned a %d-dimensional vector but CODE_GRAPH_EMBED_DIMENSION=%d", c.model, len(v), c.dimension)
			}
			return nil, fmt.Errorf("model %s returned vectors of mixed width (%d and %d) in one batch", c.model, width, len(v))
		}
	}
	return vecs, nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func truncate(b []byte) string {
	const limit = 512
	s := strings.TrimSpace(string(b))
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}
