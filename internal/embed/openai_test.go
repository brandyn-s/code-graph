package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeEmbedServer struct {
	t         *testing.T
	srv       *httptest.Server
	calls     atomic.Int32
	failFirst int32 // number of leading requests to answer with failStatus
	failCode  int
	dim       int
	reverse   bool // return data in reverse index order
	lastAuth  atomic.Value
	lastAPIK  atomic.Value
	lastBody  atomic.Value
	lastPath  atomic.Value
}

func newFakeEmbedServer(t *testing.T) *fakeEmbedServer {
	f := &fakeEmbedServer{t: t, dim: 3}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.calls.Add(1)
		f.lastPath.Store(r.URL.Path)
		f.lastAuth.Store(r.Header.Get("Authorization"))
		f.lastAPIK.Store(r.Header.Get("api-key"))
		var req openAIEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.lastBody.Store(req)
		if n <= atomic.LoadInt32(&f.failFirst) {
			w.WriteHeader(f.failCode)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		type item struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		items := make([]item, 0, len(req.Input))
		for i := range req.Input {
			v := make([]float32, f.dim)
			v[0] = float32(i) // encode position so order is checkable
			items = append(items, item{Embedding: v, Index: i})
		}
		if f.reverse {
			for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
				items[i], items[j] = items[j], items[i]
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items, "model": req.Model})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newTestOpenAI(t *testing.T, f *fakeEmbedServer, env map[string]string) *OpenAI {
	t.Helper()
	for _, k := range []string{"CODE_GRAPH_EMBED_PROVIDER", "CODE_GRAPH_EMBED_BASE_URL", "CODE_GRAPH_EMBED_API_KEY", "OPENAI_API_KEY", "CODE_GRAPH_EMBED_MODEL", "CODE_GRAPH_EMBED_DIMENSION", "CODE_GRAPH_EMBED_AUTH_HEADER", "VOYAGE_API_KEY"} {
		t.Setenv(k, "")
	}
	t.Setenv("CODE_GRAPH_EMBED_BASE_URL", f.srv.URL)
	t.Setenv("CODE_GRAPH_EMBED_MODEL", "test-embed")
	for k, v := range env {
		t.Setenv(k, v)
	}
	r := ResolveProvider(func(k string) string { return envLookup(t, k) })
	if r.Provider != ProviderOpenAI {
		t.Fatalf("resolution = %+v, want openai", r)
	}
	c := NewOpenAI(&r)
	if c == nil {
		t.Fatal("NewOpenAI returned nil")
	}
	c.retryWait = func(int, int) time.Duration { return 0 }
	return c
}

// envLookup reads the real process env (t.Setenv wrote into it).
func envLookup(t *testing.T, k string) string {
	t.Helper()
	return osGetenv(k)
}

func TestOpenAI_RequestShapeAndOrder(t *testing.T) {
	f := newFakeEmbedServer(t)
	f.reverse = true
	c := newTestOpenAI(t, f, map[string]string{"CODE_GRAPH_EMBED_API_KEY": "sk-test"})

	vecs, err := c.EmbedBatch(context.Background(), []string{"a", "b", "c"}, "document")
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if f.lastPath.Load() != "/embeddings" {
		t.Errorf("path = %v, want /embeddings", f.lastPath.Load())
	}
	if got := f.lastAuth.Load(); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q", got)
	}
	body, ok := f.lastBody.Load().(openAIEmbedRequest)
	if !ok {
		t.Fatalf("request body is %T, want openAIEmbedRequest", f.lastBody.Load())
	}
	if body.Model != "test-embed" || len(body.Input) != 3 {
		t.Errorf("body = %+v", body)
	}
	for i, v := range vecs {
		if int(v[0]) != i {
			t.Errorf("vector %d came back at position %v; order not preserved", i, v[0])
		}
	}
	if c.Model() != "test-embed" {
		t.Errorf("Model() = %q", c.Model())
	}
}

func TestOpenAI_NoCredentialSendsNoAuthHeader(t *testing.T) {
	f := newFakeEmbedServer(t)
	c := newTestOpenAI(t, f, nil)
	if _, err := c.EmbedSingle(context.Background(), "x", "query"); err != nil {
		t.Fatal(err)
	}
	if got := f.lastAuth.Load(); got != "" {
		t.Errorf("Authorization header sent without a key: %q", got)
	}
	if got := f.lastAPIK.Load(); got != "" {
		t.Errorf("api-key header sent without a key: %q", got)
	}
}

func TestOpenAI_APIKeyHeaderStyle(t *testing.T) {
	f := newFakeEmbedServer(t)
	c := newTestOpenAI(t, f, map[string]string{"OPENAI_API_KEY": "azure-key", "CODE_GRAPH_EMBED_AUTH_HEADER": "api-key"})
	if _, err := c.EmbedSingle(context.Background(), "x", "query"); err != nil {
		t.Fatal(err)
	}
	if got := f.lastAPIK.Load(); got != "azure-key" {
		t.Errorf("api-key = %q", got)
	}
	if got := f.lastAuth.Load(); got != "" {
		t.Errorf("Authorization must be empty in api-key mode, got %q", got)
	}
}

func TestOpenAI_DimensionMismatchIsAnError(t *testing.T) {
	f := newFakeEmbedServer(t)
	f.dim = 3
	c := newTestOpenAI(t, f, map[string]string{"CODE_GRAPH_EMBED_DIMENSION": "768"})
	_, err := c.EmbedSingle(context.Background(), "x", "query")
	if err == nil || !strings.Contains(err.Error(), "CODE_GRAPH_EMBED_DIMENSION=768") {
		t.Fatalf("err = %v, want dimension mismatch naming the variable", err)
	}
}

func TestOpenAI_RetriesOn429ThenSucceeds(t *testing.T) {
	f := newFakeEmbedServer(t)
	atomic.StoreInt32(&f.failFirst, 2)
	f.failCode = http.StatusTooManyRequests
	c := newTestOpenAI(t, f, nil)
	if _, err := c.EmbedSingle(context.Background(), "x", "query"); err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if n := f.calls.Load(); n != 3 {
		t.Errorf("calls = %d, want 3 (two 429s then success)", n)
	}
}

func TestOpenAI_GivesUpAfterMaxAttempts(t *testing.T) {
	f := newFakeEmbedServer(t)
	atomic.StoreInt32(&f.failFirst, 99)
	f.failCode = http.StatusInternalServerError
	c := newTestOpenAI(t, f, nil)
	_, err := c.EmbedSingle(context.Background(), "x", "query")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want a 500 error after retries", err)
	}
	if n := f.calls.Load(); n != openAIMaxAttempts {
		t.Errorf("calls = %d, want %d", n, openAIMaxAttempts)
	}
}

func TestOpenAI_ContextCancelStopsRetries(t *testing.T) {
	f := newFakeEmbedServer(t)
	atomic.StoreInt32(&f.failFirst, 99)
	f.failCode = http.StatusTooManyRequests
	c := newTestOpenAI(t, f, nil)
	ctx, cancel := context.WithCancel(context.Background())
	c.retryWait = func(int, int) time.Duration { cancel(); return time.Millisecond }
	_, err := c.EmbedSingle(ctx, "x", "query")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("calls = %d, want 1 (no retry after cancel)", n)
	}
}

func TestOpenAI_BatchesLargeInputs(t *testing.T) {
	f := newFakeEmbedServer(t)
	c := newTestOpenAI(t, f, nil)
	texts := make([]string, openAIBatchSize+5)
	for i := range texts {
		texts[i] = "t"
	}
	vecs, err := c.EmbedBatch(context.Background(), texts, "document")
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != len(texts) {
		t.Errorf("got %d vectors for %d inputs", len(vecs), len(texts))
	}
	if n := f.calls.Load(); n != 2 {
		t.Errorf("calls = %d, want 2 batches", n)
	}
}

func TestOpenAI_MalformedResponses(t *testing.T) {
	c := &OpenAI{model: "m", baseURL: "http://x"}
	cases := map[string]string{
		"wrong count":     `{"data":[{"embedding":[1],"index":0}]}`,
		"duplicate index": `{"data":[{"embedding":[1],"index":0},{"embedding":[1],"index":0}]}`,
		"empty vector":    `{"data":[{"embedding":[],"index":0},{"embedding":[1],"index":1}]}`,
		"mixed width":     `{"data":[{"embedding":[1,2],"index":0},{"embedding":[1],"index":1}]}`,
		"api error":       `{"error":{"message":"bad model"}}`,
	}
	for name, body := range cases {
		if _, err := c.parse([]byte(body), 2); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
