package store

import (
	"os"
	"strconv"
)

var memoryHalfLifeWeeks = envFloat("MEMORY_HALF_LIFE_WEEKS", 12)
var projectMemoryHalfLifeWeeks = envFloat("PROJECT_MEMORY_HALF_LIFE_WEEKS", 52)

const defaultCompactSnippetLength = 120

var compactSnippetLength = envInt("COMPACT_SNIPPET_LENGTH", defaultCompactSnippetLength)

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
