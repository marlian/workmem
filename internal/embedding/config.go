package embedding

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const (
	EnvProvider     = "WORKMEM_EMBEDDING_PROVIDER"
	EnvBaseURL      = "WORKMEM_EMBEDDING_BASE_URL"
	EnvModel        = "WORKMEM_EMBEDDING_MODEL"
	EnvDimensions   = "WORKMEM_EMBEDDING_DIMENSIONS"
	RemoteOptInFlag = "--allow-remote-embeddings"
)

type Provider string

const (
	ProviderNone             Provider = "none"
	ProviderOpenAICompatible Provider = "openai-compatible"
	ProviderOllama           Provider = "ollama"
	ProviderOpenAI           Provider = "openai"
)

type Options struct {
	Provider      string
	BaseURL       string
	Model         string
	Dimensions    int
	DimensionsRaw string
	AllowRemote   bool
}

type Config struct {
	Provider    Provider
	BaseURL     string
	Model       string
	Dimensions  int
	AllowRemote bool
}

func FromEnv() (Config, error) {
	options, err := OptionsFromEnv(os.Getenv)
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(options)
}

func OptionsFromEnv(getenv func(string) string) (Options, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	return Options{
		Provider:      getenv(EnvProvider),
		BaseURL:       getenv(EnvBaseURL),
		Model:         getenv(EnvModel),
		DimensionsRaw: getenv(EnvDimensions),
	}, nil
}

func ParseConfig(options Options) (Config, error) {
	provider, err := parseProvider(options.Provider)
	if err != nil {
		return Config{}, err
	}
	if provider == ProviderNone {
		return Config{Provider: ProviderNone}, nil
	}

	baseURL := strings.TrimSpace(options.BaseURL)
	model := strings.TrimSpace(options.Model)
	if model == "" {
		return Config{}, fmt.Errorf("embedding model is required when provider is %q", provider)
	}
	dimensions, err := resolveDimensions(options)
	if err != nil {
		return Config{}, err
	}
	if dimensions <= 0 {
		return Config{}, fmt.Errorf("embedding dimensions must be > 0 when provider is %q", provider)
	}
	parsedURL, err := parseBaseURL(baseURL)
	if err != nil {
		return Config{}, err
	}
	if provider == ProviderOpenAI && !options.AllowRemote {
		return Config{}, fmt.Errorf("embedding provider %q requires explicit remote opt-in via %s", ProviderOpenAI, RemoteOptInFlag)
	}
	if provider != ProviderOpenAI && !options.AllowRemote && !isLoopbackURL(parsedURL) {
		return Config{}, fmt.Errorf("embedding base URL host is not literal localhost or a loopback IP; set %s to allow it", RemoteOptInFlag)
	}
	return Config{
		Provider:    provider,
		BaseURL:     baseURL,
		Model:       model,
		Dimensions:  dimensions,
		AllowRemote: options.AllowRemote,
	}, nil
}

func resolveDimensions(options Options) (int, error) {
	if options.Dimensions != 0 {
		return options.Dimensions, nil
	}
	return parseOptionalNonNegativeInt(options.DimensionsRaw, EnvDimensions)
}

func parseProvider(value string) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(ProviderNone):
		return ProviderNone, nil
	case string(ProviderOpenAICompatible):
		return ProviderOpenAICompatible, nil
	case string(ProviderOllama):
		return ProviderOllama, nil
	case string(ProviderOpenAI):
		return ProviderOpenAI, nil
	default:
		return "", fmt.Errorf("unsupported embedding provider %q", strings.TrimSpace(value))
	}
}

func parseBaseURL(value string) (*url.URL, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("embedding base URL is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("embedding base URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("embedding base URL must use http or https")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("embedding base URL must not include credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("embedding base URL must not include query or fragment")
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("embedding base URL must include a host")
	}
	if err := validateBaseURLPort(parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func validateBaseURLPort(parsed *url.URL) error {
	port := parsed.Port()
	if port != "" {
		parsedPort, err := strconv.Atoi(port)
		if err != nil || parsedPort <= 0 || parsedPort > 65535 {
			return fmt.Errorf("embedding base URL port is invalid")
		}
		return nil
	}
	host := parsed.Host
	if strings.HasPrefix(host, "[") {
		closingBracket := strings.LastIndex(host, "]")
		if closingBracket == -1 {
			return fmt.Errorf("embedding base URL host is invalid")
		}
		if strings.HasPrefix(host[closingBracket+1:], ":") {
			return fmt.Errorf("embedding base URL port is invalid")
		}
		return nil
	}
	if strings.Contains(host, ":") {
		return fmt.Errorf("embedding base URL port is invalid")
	}
	return nil
}

func isLoopbackURL(parsed *url.URL) bool {
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseOptionalNonNegativeInt(value string, name string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("%s must be >= 0", name)
	}
	return parsed, nil
}
