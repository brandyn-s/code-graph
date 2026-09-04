package embed

import (
	"os"
	"strings"
	"testing"
)

// osGetenv is the getenv used by tests that drive ResolveProvider through the
// real process environment (t.Setenv).
var osGetenv = os.Getenv

func TestResolveProvider(t *testing.T) {
	cases := []struct {
		name         string
		env          map[string]string
		wantProvider string
		wantModel    string
		wantHost     string
		wantCred     bool
		wantErr      bool
		wantReason   string
	}{
		{"nothing set → off", nil, ProviderOff, "", "", false, false, "no embedding provider configured"},
		{"voyage key → voyage", map[string]string{"VOYAGE_API_KEY": "k"}, ProviderVoyage, voyageModel, "", true, false, ""},
		{"voyage key + model", map[string]string{"VOYAGE_API_KEY": "k", "VOYAGE_EMBED_MODEL": "voyage-3"}, ProviderVoyage, "voyage-3", "", true, false, ""},
		{"base url → openai", map[string]string{"CODE_GRAPH_EMBED_BASE_URL": "http://localhost:11434/v1/", "CODE_GRAPH_EMBED_MODEL": "nomic-embed-text"}, ProviderOpenAI, "nomic-embed-text", "localhost:11434", false, false, ""},
		{"voyage wins over base url in auto", map[string]string{"VOYAGE_API_KEY": "k", "CODE_GRAPH_EMBED_BASE_URL": "http://localhost:11434/v1", "CODE_GRAPH_EMBED_MODEL": "m"}, ProviderVoyage, voyageModel, "", true, false, ""},
		{"explicit openai beats voyage key", map[string]string{"VOYAGE_API_KEY": "k", "CODE_GRAPH_EMBED_PROVIDER": "openai", "CODE_GRAPH_EMBED_MODEL": "text-embedding-3-small", "OPENAI_API_KEY": "sk"}, ProviderOpenAI, "text-embedding-3-small", "api.openai.com", true, false, ""},
		{"explicit openai without model → off with error", map[string]string{"CODE_GRAPH_EMBED_PROVIDER": "openai"}, ProviderOff, "", "", false, true, "CODE_GRAPH_EMBED_MODEL is unset"},
		{"explicit voyage without key → off with error", map[string]string{"CODE_GRAPH_EMBED_PROVIDER": "voyage"}, ProviderOff, "", "", false, true, "VOYAGE_API_KEY is unset"},
		{"explicit off wins", map[string]string{"CODE_GRAPH_EMBED_PROVIDER": "off", "VOYAGE_API_KEY": "k"}, ProviderOff, "", "", false, false, "CODE_GRAPH_EMBED_PROVIDER=off"},
		{"unknown provider", map[string]string{"CODE_GRAPH_EMBED_PROVIDER": "cohere"}, ProviderOff, "", "", false, true, "unknown CODE_GRAPH_EMBED_PROVIDER"},
		{"bad base url", map[string]string{"CODE_GRAPH_EMBED_BASE_URL": "not a url", "CODE_GRAPH_EMBED_MODEL": "m"}, ProviderOff, "", "", false, true, "not an absolute http(s) URL"},
		{"bad dimension", map[string]string{"CODE_GRAPH_EMBED_BASE_URL": "http://h/v1", "CODE_GRAPH_EMBED_MODEL": "m", "CODE_GRAPH_EMBED_DIMENSION": "lots"}, ProviderOff, "", "", false, true, "CODE_GRAPH_EMBED_DIMENSION"},
		{"bad auth header", map[string]string{"CODE_GRAPH_EMBED_BASE_URL": "http://h/v1", "CODE_GRAPH_EMBED_MODEL": "m", "CODE_GRAPH_EMBED_AUTH_HEADER": "basic"}, ProviderOff, "", "", false, true, "CODE_GRAPH_EMBED_AUTH_HEADER"},
		{"embed key preferred over openai key", map[string]string{"CODE_GRAPH_EMBED_BASE_URL": "http://h/v1", "CODE_GRAPH_EMBED_MODEL": "m", "CODE_GRAPH_EMBED_API_KEY": "a", "OPENAI_API_KEY": "b"}, ProviderOpenAI, "m", "h", true, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string { return tc.env[k] }
			r := ResolveProvider(getenv)
			if r.Provider != tc.wantProvider {
				t.Fatalf("provider = %q, want %q (%+v)", r.Provider, tc.wantProvider, r)
			}
			if r.Model != tc.wantModel {
				t.Errorf("model = %q, want %q", r.Model, tc.wantModel)
			}
			if r.Host() != tc.wantHost {
				t.Errorf("host = %q, want %q", r.Host(), tc.wantHost)
			}
			if r.HasCredential != tc.wantCred {
				t.Errorf("hasCredential = %v, want %v", r.HasCredential, tc.wantCred)
			}
			if (r.Err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", r.Err, tc.wantErr)
			}
			if tc.wantReason != "" && !strings.Contains(r.Reason, tc.wantReason) {
				t.Errorf("reason = %q, want substring %q", r.Reason, tc.wantReason)
			}
			if r.Provider == ProviderOpenAI && strings.HasSuffix(r.BaseURL, "/") {
				t.Errorf("base url keeps trailing slash: %q", r.BaseURL)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	r := Resolution{Provider: ProviderOpenAI, Model: "nomic-embed-text", BaseURL: "http://localhost:11434/v1"}
	if got := r.Describe(); got != "openai (nomic-embed-text @ localhost:11434)" {
		t.Errorf("Describe = %q", got)
	}
	voyage := Resolution{Provider: ProviderVoyage, Model: "voyage-code-3"}
	if got := voyage.Describe(); got != "voyage (voyage-code-3)" {
		t.Errorf("Describe = %q", got)
	}
	off := Resolution{}
	if got := off.Describe(); got != "off" {
		t.Errorf("Describe = %q", got)
	}
}

func TestFromResolutionAndDefault(t *testing.T) {
	for _, k := range []string{"CODE_GRAPH_EMBED_PROVIDER", "CODE_GRAPH_EMBED_BASE_URL", "CODE_GRAPH_EMBED_MODEL", "VOYAGE_API_KEY", "OPENAI_API_KEY", "CODE_GRAPH_EMBED_API_KEY"} {
		t.Setenv(k, "")
	}
	if !IsDisabled(Default()) {
		t.Fatal("Default() with no configuration must be Disabled")
	}
	if !strings.Contains(ErrDisabled.Error(), "CODE_GRAPH_EMBED_BASE_URL") {
		t.Errorf("ErrDisabled should tell the user about both providers: %v", ErrDisabled)
	}
	t.Setenv("CODE_GRAPH_EMBED_BASE_URL", "http://localhost:1/v1")
	t.Setenv("CODE_GRAPH_EMBED_MODEL", "m")
	e := Default()
	if _, ok := e.(*OpenAI); !ok {
		t.Fatalf("Default() = %T, want *OpenAI", e)
	}
	if e.Model() != "m" {
		t.Errorf("Model() = %q", e.Model())
	}
	t.Setenv("VOYAGE_API_KEY", "k")
	if _, ok := Default().(*Voyage); !ok {
		t.Fatalf("Default() with VOYAGE_API_KEY = %T, want *Voyage", Default())
	}
}
