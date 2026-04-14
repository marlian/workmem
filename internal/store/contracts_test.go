package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type contractFile struct {
	Version       int               `json:"version"`
	ReferenceRepo string            `json:"reference_repo"`
	Description   string            `json:"description"`
	Cases         []json.RawMessage `json:"cases"`
}

type contractCase struct {
	ID        string            `json:"id"`
	Category  string            `json:"category"`
	Tool      string            `json:"tool"`
	Operation string            `json:"operation"`
	Setup     []json.RawMessage `json:"setup"`
	Expect    json.RawMessage   `json:"expect"`
}

func TestContractFixturesAreWellFormed(t *testing.T) {
	t.Parallel()

	paths := []string{
		filepath.Join("..", "..", "testdata", "contracts", "product-contract.json"),
		filepath.Join("..", "..", "testdata", "contracts", "implementation-detail.json"),
	}

	for _, path := range paths {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v", path, err)
			}

			var file contractFile
			if err := json.Unmarshal(data, &file); err != nil {
				t.Fatalf("json.Unmarshal(%q) error = %v", path, err)
			}
			if file.Version <= 0 {
				t.Fatalf("version = %d, want > 0", file.Version)
			}
			if file.ReferenceRepo == "" {
				t.Fatalf("reference_repo is empty")
			}
			if len(file.Cases) == 0 {
				t.Fatalf("cases is empty")
			}

			seenIDs := make(map[string]struct{}, len(file.Cases))
			for index, raw := range file.Cases {
				var item contractCase
				if err := json.Unmarshal(raw, &item); err != nil {
					t.Fatalf("json.Unmarshal(case[%d]) error = %v", index, err)
				}
				if item.ID == "" {
					t.Fatalf("case[%d] id is empty", index)
				}
				if _, exists := seenIDs[item.ID]; exists {
					t.Fatalf("duplicate case id %q", item.ID)
				}
				seenIDs[item.ID] = struct{}{}
				if item.Category == "" {
					t.Fatalf("case[%s] category is empty", item.ID)
				}
				if item.Tool == "" && item.Operation == "" {
					t.Fatalf("case[%s] must define tool or operation", item.ID)
				}
				if len(item.Expect) == 0 {
					t.Fatalf("case[%s] expect is empty", item.ID)
				}
			}
		})
	}
}
