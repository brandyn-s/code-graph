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
	"time"

	"github.com/brandyn-s/code-graph/internal/config"
)

const (
	voyageModel      = "voyage-code-3"
	voyageBatchSize  = BatchSize
	voyageBatchDelay = 1 * time.Second
)

// VoyageEmbedURL is the Voyage embeddings endpoint. Package-level var for
// test injection (same pattern as tools.releaseURL).
var VoyageEmbedURL = "https://api.voyageai.com/v1/embeddings"

// voyageEmbedRequest is the request body for Voyage /v1/embeddings.
type voyageEmbedRequest struct {
	Input     []string `json:"input"`
	Model     string   `json:"model"`
	InputType string   `json:"input_type,omitempty"`
}

// voyageEmbedResponse is the response from Voyage /v1/embeddings.
type voyageEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// Voyage embeds text via the Voyage AI API.
type Voyage struct {
	apiKey string
	model  string
	client *http.Client
}

// Compile-time check that Voyage satisfies Embedder.
var _ Embedder = (*Voyage)(nil)

// NewVoyage creates a client. Returns nil if VOYAGE_API_KEY is not set.
func NewVoyage() *Voyage {
	key := config.Get(config.VoyageAPIKey)
	if key == "" {
		return nil
	}
	model := config.Get(config.VoyageModel)
	if model == "" {
		model = voyageModel
	}
	return &Voyage{
		apiKey: key,
		model:  model,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// Model returns the embedding model this client sends to the API
// (VOYAGE_EMBED_MODEL or the package default). Exposed so tool responses
// can report the real model in provenance metadata instead of a hardcoded
// name.
func (vc *Voyage) Model() string { return vc.model }

// EmbedBatch embeds a batch of texts and returns their vectors.
// inputType should be "document" for indexing, "query" for search.
// Honors ctx cancellation: returns ctx.Err() promptly instead of continuing
// retries through batches when the caller's deadline has passed.
func (vc *Voyage) EmbedBatch(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	var allVecs [][]float32

	for i := 0; i < len(texts); i += voyageBatchSize {
		// Check cancellation before starting each inner batch so a stalled
		// outer context doesn't burn through the entire queue.
		if err := ctx.Err(); err != nil {
			return allVecs, err
		}

		end := i + voyageBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		if i > 0 {
			// Use a context-aware sleep: fail fast on cancellation.
			select {
			case <-ctx.Done():
				return allVecs, ctx.Err()
			case <-time.After(voyageBatchDelay):
			}
		}

		vecs, err := vc.embedSingleBatch(ctx, batch, inputType)
		if err != nil {
			return allVecs, fmt.Errorf("batch %d-%d: %w", i, end, err)
		}
		allVecs = append(allVecs, vecs...)
	}

	return allVecs, nil
}

// EmbedSingle embeds a single text and returns the vector.
func (vc *Voyage) EmbedSingle(ctx context.Context, text, inputType string) ([]float32, error) {
	vecs, err := vc.embedSingleBatch(ctx, []string{text}, inputType)
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("empty response from Voyage API")
	}
	return vecs[0], nil
}

func (vc *Voyage) embedSingleBatch(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	reqBody := voyageEmbedRequest{
		Input: texts,
		Model: vc.model,
	}
	if inputType != "" {
		reqBody.InputType = inputType
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	for attempt := 0; attempt < 4; attempt++ {
		// Bail out on ctx cancellation before spending another retry/sleep.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, "POST", VoyageEmbedURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+vc.apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := vc.client.Do(req)
		if err != nil {
			// Don't retry if the caller cancelled — surface the context error
			// immediately so higher layers can decide.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			if attempt < 3 {
				wait := time.Duration(1<<attempt) * time.Second
				slog.Warn("voyage.request.err", "err", err, "attempt", attempt+1, "wait", wait)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
				continue
			}
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			if attempt < 3 {
				wait := 15 * time.Duration(attempt+1) * time.Second
				if resp.StatusCode >= 500 {
					wait = time.Duration(1<<attempt) * time.Second
				}
				slog.Warn("voyage.api.retry", "status", resp.StatusCode, "attempt", attempt+1, "wait", wait)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
				continue
			}
			return nil, fmt.Errorf("Voyage API error %d: %s", resp.StatusCode, string(body))
		}

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("Voyage API error %d: %s", resp.StatusCode, string(body))
		}

		var embedResp voyageEmbedResponse
		if err := json.Unmarshal(body, &embedResp); err != nil {
			return nil, fmt.Errorf("parse response: %w", err)
		}

		// Sort by index to maintain input order
		vecs := make([][]float32, len(embedResp.Data))
		for _, item := range embedResp.Data {
			vecs[item.Index] = item.Embedding
		}

		return vecs, nil
	}

	return nil, fmt.Errorf("exhausted retries")
}
