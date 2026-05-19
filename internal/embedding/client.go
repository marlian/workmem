package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Client interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type httpEmbedClient struct {
	cfg        Config
	httpClient *http.Client
	endpoint   string
}

func NewClient(cfg Config, httpClient *http.Client) (Client, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	switch cfg.Provider {
	case ProviderOpenAICompatible:
		return &httpEmbedClient{cfg: cfg, httpClient: httpClient, endpoint: appendURLPath(cfg.BaseURL, "embeddings")}, nil
	case ProviderOllama:
		return &httpEmbedClient{cfg: cfg, httpClient: httpClient, endpoint: appendURLPath(cfg.BaseURL, "api/embed")}, nil
	case ProviderNone:
		return nil, fmt.Errorf("embedding provider %q has no client", ProviderNone)
	case ProviderOpenAI:
		return nil, fmt.Errorf("embedding provider %q is not supported by semantic report mode", ProviderOpenAI)
	default:
		return nil, fmt.Errorf("unsupported embedding provider %q", cfg.Provider)
	}
}

func (c *httpEmbedClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	switch c.cfg.Provider {
	case ProviderOpenAICompatible:
		return c.embedOpenAICompatible(ctx, texts)
	case ProviderOllama:
		return c.embedOllama(ctx, texts)
	default:
		return nil, fmt.Errorf("unsupported embedding provider %q", c.cfg.Provider)
	}
}

func (c *httpEmbedClient) embedOpenAICompatible(ctx context.Context, texts []string) ([][]float32, error) {
	body := map[string]any{
		"model": c.cfg.Model,
		"input": texts,
	}
	var response struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := c.postJSON(ctx, body, &response); err != nil {
		return nil, err
	}
	if len(response.Data) != len(texts) {
		return nil, fmt.Errorf("embedding response count mismatch")
	}
	sort.Slice(response.Data, func(i, j int) bool { return response.Data[i].Index < response.Data[j].Index })
	vectors := make([][]float32, len(response.Data))
	for i, item := range response.Data {
		if item.Index != i {
			return nil, fmt.Errorf("embedding response index mismatch")
		}
		if err := validateVector(item.Embedding, c.cfg.Dimensions); err != nil {
			return nil, err
		}
		vectors[i] = item.Embedding
	}
	return vectors, nil
}

func (c *httpEmbedClient) embedOllama(ctx context.Context, texts []string) ([][]float32, error) {
	body := map[string]any{
		"model": c.cfg.Model,
		"input": texts,
	}
	var response struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := c.postJSON(ctx, body, &response); err != nil {
		return nil, err
	}
	if len(response.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embedding response count mismatch")
	}
	for _, vector := range response.Embeddings {
		if err := validateVector(vector, c.cfg.Dimensions); err != nil {
			return nil, err
		}
	}
	return response.Embeddings, nil
}

func (c *httpEmbedClient) postJSON(ctx context.Context, requestBody any, responseBody any) error {
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("marshal embedding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("embedding request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("embedding request failed with status %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 16<<20))
	if err := decoder.Decode(responseBody); err != nil {
		return fmt.Errorf("decode embedding response: %w", err)
	}
	return nil
}

func appendURLPath(baseURL string, segment string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(segment, "/")
	}
	basePath := cleanURLPath(parsed.Path)
	segment = strings.Trim(segment, "/")
	if segment == "" {
		parsed.Path = basePath
	} else if basePath == "" {
		parsed.Path = "/" + segment
	} else if strings.HasSuffix(basePath, "/"+segment) {
		parsed.Path = basePath
	} else {
		parsed.Path = basePath + "/" + segment
	}
	return parsed.String()
}
