// Package anthropic is a minimal HTTP client for the Anthropic Messages
// API with tool-use support. Modeled on internal/pipeline/voyage_client.go
// — raw HTTP, no SDK, retry on 429/5xx with exponential backoff.
//
// Used by internal/locagent for the LocAgent-style agent loop on top of
// our MCP graph primitives.
package anthropic

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

const (
	messagesURL      = "https://api.anthropic.com/v1/messages"
	apiVersion       = "2023-06-01"
	defaultModel     = "claude-haiku-4-5-20251001" // fast model for tool-use loops
	defaultMaxTokens = 4096
	defaultTimeout   = 120 * time.Second
)

// Client wraps the Anthropic Messages API.
type Client struct {
	apiKey string
	model  string
	client *http.Client
}

// NewClient returns a configured client. Returns nil if ANTHROPIC_API_KEY
// is not set, matching the VoyageClient nil-on-missing-key pattern so
// callers can degrade gracefully instead of crashing.
func NewClient() *Client {
	key := config.Get(config.AnthropicAPIKey)
	if key == "" {
		return nil
	}
	model := SanitizeModelID(config.Get(config.AnthropicModel))
	if model == "" {
		model = defaultModel
	}
	return &Client{
		apiKey: key,
		model:  model,
		client: &http.Client{Timeout: defaultTimeout},
	}
}

// SanitizeModelID strips Claude Code session-notation suffixes from an
// inherited model id. Claude Code launchers pin ANTHROPIC_MODEL for the
// host session using bracket beta markers (e.g. "claude-sonnet-5[1m]" for
// the 1M-context variant); MCP servers spawned by that session inherit the
// env verbatim, and the raw string 404s against the Messages API
// (observed live 2026-07-04: not_found_error "model: claude-sonnet-5[1m]"
// broke every code_localize_agent call on the host). The base id before
// the bracket is the valid API model.
func SanitizeModelID(model string) string {
	model = strings.TrimSpace(model)
	if i := strings.IndexByte(model, '['); i > 0 {
		model = strings.TrimSpace(model[:i])
	}
	return model
}

// Model returns the configured model name.
func (c *Client) Model() string { return c.model }

// Tool describes a tool the model can call.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Message is a single conversation turn.
type Message struct {
	Role    string         `json:"role"` // "user" | "assistant"
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one element of message content. Either Text, ToolUse,
// or ToolResult — at most one of these is non-zero in a given block.
//
// Note on tool_result content: the Anthropic API expects `content` to be
// a string OR an array of content blocks (with type="text" or "image").
// We use a string (the simplest encoding); callers should JSON-marshal
// any structured tool output and pass the resulting string.
type ContentBlock struct {
	Type string `json:"type"`

	// type=text
	Text string `json:"text,omitempty"`

	// type=tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type=tool_result
	ToolUseID         string `json:"tool_use_id,omitempty"`
	IsError           bool   `json:"is_error,omitempty"`
	ToolResultContent string `json:"content,omitempty"`
}

// MessagesRequest is the request body.
type MessagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Tools     []Tool    `json:"tools,omitempty"`
}

// MessagesResponse is the response body.
type MessagesResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"` // "end_turn", "tool_use", "max_tokens", ...
	Content    []ContentBlock `json:"content"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// CreateMessage sends a request and returns the response, with retry on
// 429/5xx (4 attempts, exponential backoff).
func (c *Client) CreateMessage(ctx context.Context, req MessagesRequest) (*MessagesResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = defaultMaxTokens
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	for attempt := 0; attempt < 4; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		httpReq, err := http.NewRequestWithContext(ctx, "POST", messagesURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("x-api-key", c.apiKey)
		httpReq.Header.Set("anthropic-version", apiVersion)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := c.client.Do(httpReq)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			if attempt < 3 {
				wait := time.Duration(1<<attempt) * time.Second
				slog.Warn("anthropic.request.err", "err", err, "attempt", attempt+1, "wait", wait)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
				continue
			}
			// Retries exhausted on a connection/network error.
			return nil, fmt.Errorf("%w: %v", ErrTimeoutExhausted, err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			if attempt < 3 {
				wait := 5 * time.Duration(attempt+1) * time.Second
				if resp.StatusCode >= 500 {
					wait = time.Duration(1<<attempt) * time.Second
				}
				slog.Warn("anthropic.api.retry", "status", resp.StatusCode, "attempt", attempt+1, "wait", wait, "body", truncate(string(body), 200))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
				continue
			}
			// Retries exhausted — wrap with typed sentinel so callers can
			// distinguish rate-limit vs server-error programmatically.
			sentinel := ErrServerError
			if resp.StatusCode == 429 {
				sentinel = ErrRateLimitExhausted
			}
			return nil, fmt.Errorf("%w (status %d): %s", sentinel, resp.StatusCode, string(body))
		}
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			// Auth failures are deterministic — don't retry. Wrap with
			// typed sentinel so callers can distinguish auth failure from
			// other 4xx errors without parsing error text.
			return nil, fmt.Errorf("%w (status %d): %s", ErrAuthFailed, resp.StatusCode, string(body))
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(body))
		}

		var out MessagesResponse
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("parse response: %w (body: %s)", err, truncate(string(body), 500))
		}
		return &out, nil
	}
	return nil, fmt.Errorf("exhausted retries")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
