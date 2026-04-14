package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type productContractFile struct {
	Cases []productContractCase `json:"cases"`
}

type productContractCase struct {
	ID     string                  `json:"id"`
	Setup  []productContractAction `json:"setup"`
	Tool   string                  `json:"tool"`
	Args   map[string]any          `json:"args"`
	Expect productContractExpect   `json:"expect"`
}

type productContractAction struct {
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
	CaptureAs string         `json:"capture_as"`
}

type productContractExpect struct {
	Stored                    *bool          `json:"stored"`
	Project                   any            `json:"project"`
	RequiredFields            []string       `json:"required_fields"`
	TotalMin                  int            `json:"total_min"`
	ContainsEntity            any            `json:"contains_entity"`
	Deleted                   *bool          `json:"deleted"`
	Type                      string         `json:"type"`
	Compact                   *bool          `json:"compact"`
	ObservationFields         []string       `json:"observation_fields"`
	Truncated                 *bool          `json:"truncated"`
	FollowUp                  any            `json:"follow_up"`
	Scope                     string         `json:"scope"`
	ExpectAbsentObservationID string         `json:"expect_absent_observation_id"`
	ExpectAbsentEntity        string         `json:"expect_absent_entity"`
	Expect                    map[string]any `json:"expect"`
}

func TestProductContractFixturesReplayAgainstGo(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", "product-contract.json"))
	if err != nil {
		t.Fatalf("ReadFile(product-contract.json) error = %v", err)
	}

	var file productContractFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("json.Unmarshal(product-contract.json) error = %v", err)
	}

	for _, testCase := range file.Cases {
		testCase := testCase
		t.Run(testCase.ID, func(t *testing.T) {
			db := newTestDB(t, testCase.ID+".db")
			captures := map[string]any{}

			for _, action := range testCase.Setup {
				result := runContractTool(t, db, action.Tool, materializeArgs(action.Args, captures))
				if action.CaptureAs != "" {
					captures[action.CaptureAs] = extractObservationID(t, result)
				}
			}

			result := runContractTool(t, db, testCase.Tool, materializeArgs(testCase.Args, captures))
			assertContractCase(t, db, testCase, result, captures)
		})
	}
}

func runContractTool(t *testing.T, db *sql.DB, name string, args ToolArgs) any {
	t.Helper()
	result, err := HandleTool(db, name, args)
	if err != nil {
		t.Fatalf("HandleTool(%s) error = %v", name, err)
	}
	return result
}

func materializeArgs(input map[string]any, captures map[string]any) ToolArgs {
	args := ToolArgs{}
	for key, raw := range input {
		switch key {
		case "entity":
			args.Entity = raw.(string)
		case "entity_type":
			args.EntityType = raw.(string)
		case "observation":
			args.Observation = raw.(string)
		case "project":
			args.Project = raw.(string)
		case "query":
			args.Query = raw.(string)
		case "compact":
			args.Compact = raw.(bool)
		case "limit":
			limit := int(raw.(float64))
			args.Limit = &limit
		case "observation_id":
			switch value := raw.(type) {
			case string:
				resolved := captures[value[1:]].(int64)
				args.ObservationID = &resolved
			case float64:
				resolved := int64(value)
				args.ObservationID = &resolved
			}
		}
	}
	return args
}

func extractObservationID(t *testing.T, result any) int64 {
	t.Helper()
	remember, ok := result.(RememberResult)
	if !ok {
		t.Fatalf("expected RememberResult, got %T", result)
	}
	return remember.ObservationID
}

func assertContractCase(t *testing.T, db *sql.DB, testCase productContractCase, result any, captures map[string]any) {
	t.Helper()
	switch testCase.ID {
	case "remember-basic":
		remember := result.(RememberResult)
		if !remember.Stored || remember.EntityID == 0 || remember.ObservationID == 0 {
			t.Fatalf("remember-basic drifted: %#v", remember)
		}
	case "recall-basic":
		recall := result.(RecallResponse)
		if recall.TotalFacts < 1 {
			t.Fatalf("recall-basic returned no facts")
		}
		found := false
		for _, group := range recall.Results {
			if group.EntityName == "TestEntity" {
				found = true
			}
		}
		if !found {
			t.Fatalf("recall-basic missing TestEntity")
		}
	case "recall-compact-truncates-and-flags":
		recall := result.(RecallResponse)
		if !recall.Compact || len(recall.Results) == 0 || len(recall.Results[0].Observations) == 0 {
			t.Fatalf("compact recall response drifted: %#v", recall)
		}
		obs := recall.Results[0].Observations[0]
		if !obs.Truncated || obs.CompositeScore == 0 || obs.Confidence == 0 {
			t.Fatalf("compact recall observation drifted: %#v", obs)
		}
	case "forget-observation-hides-from-recall":
		forget := result.(ForgetResult)
		if !forget.Deleted || forget.Type != "observation" {
			t.Fatalf("forget observation drifted: %#v", forget)
		}
		followUp := runContractTool(t, db, "recall", ToolArgs{Query: "observation to forget", Limit: intPointer(5)})
		recall := followUp.(RecallResponse)
		deletedID := captures["observation_id"].(int64)
		for _, group := range recall.Results {
			for _, obs := range group.Observations {
				if obs.ID == deletedID {
					t.Fatalf("deleted observation still present in recall")
				}
			}
		}
	case "forget-entity-hides-from-list-and-recall-entity":
		forget := result.(ForgetResult)
		if !forget.Deleted || forget.Type != "entity" {
			t.Fatalf("forget entity drifted: %#v", forget)
		}
		listAny := runContractTool(t, db, "list_entities", ToolArgs{})
		list := listAny.(ListEntitiesResult)
		for _, entity := range list.Entities {
			if entity.Name == "EntityToForget" {
				t.Fatalf("EntityToForget still listed after forget")
			}
		}
		recallEntityAny := runContractTool(t, db, "recall_entity", ToolArgs{Entity: "EntityToForget"})
		recallEntity := recallEntityAny.(RecallEntityResult)
		if recallEntity.Found {
			t.Fatalf("EntityToForget still found after forget")
		}
	case "project-isolation-no-global-leak":
		recall := result.(RecallResponse)
		for _, group := range recall.Results {
			if group.EntityName == "ProjectOnly" {
				t.Fatalf("project fact leaked into global recall")
			}
		}
	default:
		t.Fatalf("unhandled contract case %q", testCase.ID)
	}
}

func intPointer(value int) *int {
	return &value
}
