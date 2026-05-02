package embedding

import (
	"strings"
	"testing"
)

func TestParseConfigDefaultsToNone(t *testing.T) {
	cfg, err := ParseConfig(Options{})
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	if cfg.Provider != ProviderNone {
		t.Fatalf("provider = %q, want %q", cfg.Provider, ProviderNone)
	}
	if cfg.BaseURL != "" || cfg.Model != "" || cfg.Dimensions != 0 || cfg.AllowRemote {
		t.Fatalf("default config = %#v, want zero-value non-provider fields", cfg)
	}
}

func TestParseConfigRejectsUnknownProvider(t *testing.T) {
	_, err := ParseConfig(Options{Provider: "mystery"})
	if err == nil {
		t.Fatalf("ParseConfig(unknown provider) error = nil, want error")
	}
}

func TestParseConfigAcceptsLoopbackOpenAICompatible(t *testing.T) {
	for _, baseURL := range []string{"http://127.0.0.1:1235/v1", "http://localhost:1235/v1", "http://[::1]:1235/v1"} {
		t.Run(baseURL, func(t *testing.T) {
			cfg, err := ParseConfig(Options{
				Provider:   string(ProviderOpenAICompatible),
				BaseURL:    baseURL,
				Model:      "text-embedding-finetuned-bge-m3",
				Dimensions: 1024,
			})
			if err != nil {
				t.Fatalf("ParseConfig(loopback openai-compatible) error = %v", err)
			}
			if cfg.Provider != ProviderOpenAICompatible || cfg.Dimensions != 1024 {
				t.Fatalf("config = %#v, want openai-compatible dimensions 1024", cfg)
			}
		})
	}
}

func TestParseConfigRejectsRemoteOpenAIWithoutOptIn(t *testing.T) {
	_, err := ParseConfig(Options{
		Provider:   string(ProviderOpenAI),
		BaseURL:    "https://api.openai.example/v1",
		Model:      "text-embedding-3-large",
		Dimensions: 3072,
	})
	if err == nil {
		t.Fatalf("ParseConfig(remote openai without opt-in) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "remote opt-in") {
		t.Fatalf("error = %v, want remote opt-in message", err)
	}
}

func TestParseConfigAcceptsRemoteOpenAIWithOptIn(t *testing.T) {
	cfg, err := ParseConfig(Options{
		Provider:    string(ProviderOpenAI),
		BaseURL:     "https://api.openai.example/v1",
		Model:       "text-embedding-3-large",
		Dimensions:  3072,
		AllowRemote: true,
	})
	if err != nil {
		t.Fatalf("ParseConfig(remote openai with opt-in) error = %v", err)
	}
	if cfg.Provider != ProviderOpenAI || !cfg.AllowRemote {
		t.Fatalf("config = %#v, want remote openai with opt-in", cfg)
	}
}

func TestParseConfigRejectsRemoteLocalProvidersWithoutOptIn(t *testing.T) {
	for _, provider := range []Provider{ProviderOpenAICompatible, ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			_, err := ParseConfig(Options{
				Provider:   string(provider),
				BaseURL:    "https://embeddings.example/v1",
				Model:      "local-model",
				Dimensions: 1024,
			})
			if err == nil {
				t.Fatalf("ParseConfig(%s remote URL without opt-in) error = nil, want error", provider)
			}
			if !strings.Contains(err.Error(), "not loopback") {
				t.Fatalf("error = %v, want loopback message", err)
			}
		})
	}
}

func TestParseConfigRejectsCredentialedBaseURL(t *testing.T) {
	_, err := ParseConfig(Options{
		Provider:   string(ProviderOpenAICompatible),
		BaseURL:    "http://user:secret@127.0.0.1:1235/v1",
		Model:      "local-model",
		Dimensions: 1024,
	})
	if err == nil {
		t.Fatalf("ParseConfig(credentialed URL) error = nil, want error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked URL credential: %v", err)
	}
}

func TestParseConfigDoesNotLeakMalformedURLCredentials(t *testing.T) {
	_, err := ParseConfig(Options{
		Provider:   string(ProviderOpenAICompatible),
		BaseURL:    "http://user:secret@%zz/v1",
		Model:      "local-model",
		Dimensions: 1024,
	})
	if err == nil {
		t.Fatalf("ParseConfig(malformed credentialed URL) error = nil, want error")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "user") {
		t.Fatalf("error leaked URL credential: %v", err)
	}
}

func TestOptionsFromEnv(t *testing.T) {
	values := map[string]string{
		EnvProvider:   string(ProviderOpenAICompatible),
		EnvBaseURL:    "http://localhost:1235/v1",
		EnvModel:      "text-embedding-finetuned-bge-m3",
		EnvDimensions: "1024",
	}
	options, err := OptionsFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("OptionsFromEnv() error = %v", err)
	}
	cfg, err := ParseConfig(options)
	if err != nil {
		t.Fatalf("ParseConfig(OptionsFromEnv()) error = %v", err)
	}
	if cfg.Provider != ProviderOpenAICompatible || cfg.Dimensions != 1024 {
		t.Fatalf("config = %#v, want env-derived openai-compatible", cfg)
	}
}

func TestOptionsFromEnvDefersDimensionValidationUntilProviderRequiresIt(t *testing.T) {
	values := map[string]string{
		EnvProvider:   string(ProviderNone),
		EnvDimensions: "not-an-int",
	}
	options, err := OptionsFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("OptionsFromEnv(provider none with bad dimensions) error = %v", err)
	}
	cfg, err := ParseConfig(options)
	if err != nil {
		t.Fatalf("ParseConfig(provider none with bad dimensions) error = %v", err)
	}
	if cfg.Provider != ProviderNone {
		t.Fatalf("provider = %q, want %q", cfg.Provider, ProviderNone)
	}
}

func TestParseConfigRejectsInvalidDimensionEnvWhenProviderUsesEmbeddings(t *testing.T) {
	options, err := OptionsFromEnv(func(key string) string {
		values := map[string]string{
			EnvProvider:   string(ProviderOpenAICompatible),
			EnvBaseURL:    "http://localhost:1235/v1",
			EnvModel:      "text-embedding-finetuned-bge-m3",
			EnvDimensions: "not-an-int",
		}
		return values[key]
	})
	if err != nil {
		t.Fatalf("OptionsFromEnv() error = %v", err)
	}
	_, err = ParseConfig(options)
	if err == nil {
		t.Fatalf("ParseConfig(non-none provider with bad env dimensions) error = nil, want error")
	}
}

func TestParseConfigExplicitDimensionsOverrideRawDimensions(t *testing.T) {
	cfg, err := ParseConfig(Options{
		Provider:      string(ProviderOpenAICompatible),
		BaseURL:       "http://localhost:1235/v1",
		Model:         "text-embedding-finetuned-bge-m3",
		Dimensions:    1024,
		DimensionsRaw: "not-an-int",
	})
	if err != nil {
		t.Fatalf("ParseConfig(explicit dimensions override raw) error = %v", err)
	}
	if cfg.Dimensions != 1024 {
		t.Fatalf("dimensions = %d, want 1024", cfg.Dimensions)
	}
}

func TestOptionsFromEnvDoesNotEnableRemoteOptIn(t *testing.T) {
	values := map[string]string{
		EnvProvider:                      string(ProviderOpenAI),
		EnvBaseURL:                       "https://api.openai.example/v1",
		EnvModel:                         "text-embedding-3-large",
		EnvDimensions:                    "3072",
		"WORKMEM_EMBEDDING_ALLOW_REMOTE": "true",
	}
	options, err := OptionsFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("OptionsFromEnv() error = %v", err)
	}
	if options.AllowRemote {
		t.Fatalf("OptionsFromEnv().AllowRemote = true, want false; remote opt-in must be explicit CLI config")
	}
	_, err = ParseConfig(options)
	if err == nil {
		t.Fatalf("ParseConfig(env remote opt-in only) error = nil, want fail closed")
	}
}
