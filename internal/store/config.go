package store

import (
	"os"
	"strconv"
)

const defaultCompactSnippetLength = 120

// Config getters read the process environment on every call so that values
// injected late (e.g., by the dotenv loader in main before mcpserver.New)
// are honored. The per-call cost is a map lookup plus a parse — negligible
// compared to the work each tool handler does.

func memoryHalfLifeWeeks() float64 {
	return envFloat("MEMORY_HALF_LIFE_WEEKS", 12)
}

func projectMemoryHalfLifeWeeks() float64 {
	return envFloat("PROJECT_MEMORY_HALF_LIFE_WEEKS", 52)
}

func compactSnippetLength() int {
	return envInt("COMPACT_SNIPPET_LENGTH", defaultCompactSnippetLength)
}

func envFloat(key string, fallback float64) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
