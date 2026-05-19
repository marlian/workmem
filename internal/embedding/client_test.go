package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompatibleClientEmbedsBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("path = %q, want /v1/embeddings", r.URL.Path)
		}
		var request struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode(request) error = %v", err)
		}
		if request.Model != "local-model" || len(request.Input) != 2 {
			t.Fatalf("request = %#v, want model and 2 inputs", request)
		}
		_, _ = w.Write([]byte(`{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`))
	}))
	defer server.Close()

	cfg, err := ParseConfig(Options{Provider: string(ProviderOpenAICompatible), BaseURL: server.URL + "/v1", Model: "local-model", Dimensions: 2, AllowRemote: true})
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	client, err := NewClient(cfg, server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	vectors, err := client.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if vectors[0][0] != 1 || vectors[1][1] != 1 {
		t.Fatalf("vectors = %#v, want index-sorted response", vectors)
	}
}

func TestOllamaClientEmbedsBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Fatalf("path = %q, want /api/embed", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"embeddings":[[1,0],[0,1]]}`))
	}))
	defer server.Close()

	cfg, err := ParseConfig(Options{Provider: string(ProviderOllama), BaseURL: server.URL, Model: "local-model", Dimensions: 2, AllowRemote: true})
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	client, err := NewClient(cfg, server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	vectors, err := client.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][1] != 1 {
		t.Fatalf("vectors = %#v, want ollama embeddings", vectors)
	}
}

func TestClientRejectsBadProviderAndBadResponses(t *testing.T) {
	if _, err := NewClient(Config{Provider: ProviderNone}, nil); err == nil {
		t.Fatalf("NewClient(none) error = nil, want error")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "secret body should not leak", http.StatusInternalServerError)
	}))
	defer server.Close()
	cfg, err := ParseConfig(Options{Provider: string(ProviderOpenAICompatible), BaseURL: server.URL + "/v1", Model: "local-model", Dimensions: 2, AllowRemote: true})
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	client, err := NewClient(cfg, server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatalf("Embed(non-2xx) error = nil, want error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked response body: %v", err)
	}
}

func TestClientWrapsTransportErrors(t *testing.T) {
	cfg, err := ParseConfig(Options{Provider: string(ProviderOpenAICompatible), BaseURL: "http://localhost:1235/v1", Model: "local-model", Dimensions: 2})
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	client, err := NewClient(cfg, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic dial failure")
	})})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatalf("Embed(transport error) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "embedding request failed") || !strings.Contains(err.Error(), "synthetic dial failure") {
		t.Fatalf("transport error = %v, want wrapped cause", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
