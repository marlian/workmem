package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"workmem/internal/store"
	"workmem/internal/telemetry"
)

const serverVersion = "0.1.0"

// Config carries the construction parameters for a Runtime. Ownership of the
// Telemetry client transfers to the Runtime: Runtime.Close() will close it.
// Callers must not call Close on the client themselves once it has been
// handed to New.
type Config struct {
	DBPath    string
	Telemetry *telemetry.Client
}

type Runtime struct {
	server    *mcp.Server
	defaultDB *sql.DB
	dbPath    string
	// telemetry is stored as an atomic.Pointer so concurrent reads by
	// in-flight tool handlers cannot race with the Close-time Swap during
	// shutdown. Reads use Load(); Close uses Swap(nil) to atomically take
	// ownership of the pointer for cleanup.
	telemetry atomic.Pointer[telemetry.Client]
}

type toolDefinition struct {
	Name        string
	Description string
	Required    []string
	InputSchema map[string]any
}

func New(config Config) (*Runtime, error) {
	dbPath, err := ResolveDBPath(config.DBPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := store.InitDB(dbPath)
	if err != nil {
		return nil, err
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "memory",
		Version: serverVersion,
	}, nil)

	runtime := &Runtime{server: server, defaultDB: db, dbPath: dbPath}
	runtime.telemetry.Store(config.Telemetry)
	runtime.registerTools()
	return runtime, nil
}

func (r *Runtime) Run(ctx context.Context, transport mcp.Transport) error {
	defer r.Close()
	err := r.server.Run(ctx, transport)
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, mcp.ErrConnectionClosed) {
		return nil
	}
	return err
}

func (r *Runtime) RunStdio(ctx context.Context) error {
	return r.Run(ctx, &mcp.StdioTransport{})
}

func (r *Runtime) Close() error {
	var closeErr error
	if r.defaultDB != nil {
		closeErr = r.defaultDB.Close()
		r.defaultDB = nil
	}
	// atomic.Pointer.Swap atomically takes the old pointer and installs nil,
	// so any in-flight handler that reaches its telemetry defer during
	// shutdown observes a nil Load() and skips the log path entirely. No
	// data race between this write and the concurrent reads in handleTool.
	// The underlying Client is also mutex-guarded (see telemetry/client.go),
	// so even if a handler already captured the old pointer before the swap
	// its Log* calls will serialize with the Close below.
	var teleErr error
	if telemetryClient := r.telemetry.Swap(nil); telemetryClient != nil {
		teleErr = telemetryClient.Close()
	}
	return errors.Join(closeErr, teleErr, store.ResetProjectDBs())
}

func (r *Runtime) DBPath() string {
	return r.dbPath
}

func (r *Runtime) registerTools() {
	for _, def := range toolDefinitions() {
		def := def
		r.server.AddTool(&mcp.Tool{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.InputSchema,
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return r.handleTool(ctx, def, req)
		})
	}
}

func (r *Runtime) handleTool(_ context.Context, def toolDefinition, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Telemetry observables — captured as the flow unfolds. The telemetry
	// client pointer is read via atomic.Pointer.Load() once and captured
	// into the closure below; a concurrent Runtime.Close() Swap cannot race
	// with the captured value. When telemetry is disabled (Load returns
	// nil) we skip both the time.Now/time.Since measurement and the defer
	// installation entirely, so the no-telemetry path stays near-zero.
	var (
		argObject  map[string]any
		toolResult any
		metrics    *store.SearchMetrics
		projectRaw string
		isError    bool
	)
	if tele := r.telemetry.Load(); tele != nil {
		t0 := time.Now()
		defer func() {
			id := r.logToolCall(tele, def.Name, req, argObject, toolResult, projectRaw, isError, time.Since(t0))
			r.logSearchMetrics(tele, id, metrics)
		}()
	}

	raw := req.Params.Arguments
	if len(raw) == 0 {
		raw = []byte("{}")
	}

	var err error
	argObject, err = parseArgumentObject(raw)
	if err != nil {
		isError = true
		return errorResult(map[string]any{
			"error": err.Error(),
			"tool":  def.Name,
		}), nil
	}
	if p, ok := argObject["project"].(string); ok {
		projectRaw = p
	}

	missing := missingRequiredArguments(def.Required, argObject)
	if len(missing) > 0 {
		isError = true
		return errorResult(map[string]any{
			"error":          fmt.Sprintf("Missing required arguments: %s", strings.Join(missing, ", ")),
			"tool":           def.Name,
			"RetryArguments": retryArguments(def),
		}), nil
	}

	if validation := validateStringArguments(def, argObject); validation != nil {
		isError = true
		return errorResult(validation), nil
	}

	var args store.ToolArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		isError = true
		return errorResult(map[string]any{
			"error": fmt.Sprintf("Invalid arguments: %v", err),
			"tool":  def.Name,
			"hint":  "Check argument types and values. Use list_entities to discover valid entity names.",
		}), nil
	}

	toolResult, metrics, err = store.HandleToolWithMetrics(r.defaultDB, def.Name, args)
	if err != nil {
		isError = true
		toolResult = nil
		return errorResult(map[string]any{
			"error": err.Error(),
			"tool":  def.Name,
			"hint":  "Check argument types and values. Use list_entities to discover valid entity names.",
		}), nil
	}

	return successResult(toolResult)
}

func ResolveDBPath(configPath string) (string, error) {
	if configPath != "" {
		return filepath.Clean(configPath), nil
	}
	if envPath := strings.TrimSpace(os.Getenv("MEMORY_DB_PATH")); envPath != "" {
		return filepath.Clean(envPath), nil
	}

	execPath, err := os.Executable()
	if err == nil && !looksLikeGoRunPath(execPath) {
		return filepath.Join(filepath.Dir(execPath), "memory.db"), nil
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory for default db path: %w", err)
	}
	return filepath.Join(workingDir, "memory.db"), nil
}

func looksLikeGoRunPath(path string) bool {
	clean := filepath.ToSlash(path)
	return strings.Contains(clean, "/go-build")
}

func parseArgumentObject(raw json.RawMessage) (map[string]any, error) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("Invalid JSON arguments: %w", err)
	}
	if decoded == nil {
		return map[string]any{}, nil
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("Tool arguments must be a JSON object")
	}
	return object, nil
}

func missingRequiredArguments(required []string, args map[string]any) []string {
	missing := make([]string, 0)
	for _, key := range required {
		value, ok := args[key]
		if !ok || value == nil {
			missing = append(missing, key)
		}
	}
	return missing
}

func validateStringArguments(def toolDefinition, args map[string]any) map[string]any {
	for _, key := range []string{"entity", "observation", "query", "from", "to", "relation_type", "project", "label", "event_date", "event_type", "date_from", "date_to", "entity_type", "context", "expires_at", "source"} {
		value, ok := args[key]
		if !ok || value == nil {
			continue
		}
		if _, ok := value.(string); ok {
			continue
		}
		return map[string]any{
			"error": fmt.Sprintf("Invalid argument type: '%s' must be a string, got %T", key, value),
			"tool":  def.Name,
			"received": map[string]any{
				key: value,
			},
			"RetryArguments": map[string]string{
				key: fmt.Sprintf("string — %s", propertyDescription(def, key)),
			},
		}
	}
	return nil
}

func retryArguments(def toolDefinition) map[string]string {
	properties, _ := def.InputSchema["properties"].(map[string]any)
	retry := make(map[string]string, len(properties))
	for key := range properties {
		retry[key] = propertyDescription(def, key)
	}
	return retry
}

func propertyDescription(def toolDefinition, key string) string {
	properties, _ := def.InputSchema["properties"].(map[string]any)
	property, _ := properties[key].(map[string]any)
	description, _ := property["description"].(string)
	if description == "" {
		return "see schema"
	}
	return description
}

func successResult(result any) (*mcp.CallToolResult, error) {
	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal tool result: %w", err)
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(payload)}},
		StructuredContent: result,
	}, nil
}

func errorResult(payload map[string]any) *mcp.CallToolResult {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		encoded = []byte(`{"error":"failed to marshal error payload"}`)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		IsError: true,
	}
}

func toolDefinitions() []toolDefinition {
	return []toolDefinition{
		{
			Name:        "remember",
			Description: "Store a fact about an entity. Creates the entity if it does not exist, appends observation if it does. Use this proactively whenever you learn something worth retaining across sessions.",
			Required:    []string{"entity", "observation"},
			InputSchema: objectSchema(map[string]any{
				"entity":      stringProperty(`Entity name (e.g. "Alice", "ProjectX", "React")`),
				"entity_type": stringProperty(`Optional type (e.g. "person", "project", "technology")`),
				"observation": stringProperty("The fact to remember — one atomic piece of information"),
				"source":      stringProperty(`Where this fact comes from (default: user). Suggested values: "user", "inferred", "session"`),
				"confidence":  numberProperty("Confidence 0.0-1.0 (default: 1.0 for user-stated facts)"),
				"event_id":    numberProperty("Optional event ID to attach this observation to (from remember_event)"),
				"project":     stringProperty("Project workspace path for project-scoped memory (absolute or relative to ~). Omit for global memory."),
			}, "entity", "observation"),
		},
		{
			Name:        "remember_batch",
			Description: "Store multiple facts at once. Each item creates/updates an entity and appends an observation.",
			Required:    []string{"facts"},
			InputSchema: objectSchema(map[string]any{
				"facts": map[string]any{
					"type":        "array",
					"description": "Array of facts to store",
					"items": objectSchema(map[string]any{
						"entity":      map[string]any{"type": "string"},
						"entity_type": map[string]any{"type": "string"},
						"observation": map[string]any{"type": "string"},
						"source":      map[string]any{"type": "string"},
						"confidence":  map[string]any{"type": "number"},
						"event_id":    numberProperty("Optional event ID to attach this observation to"),
					}, "entity", "observation"),
				},
				"project": stringProperty("Project workspace path for project-scoped memory (absolute or relative to ~). Omit for global memory."),
			}, "facts"),
		},
		{
			Name:        "recall",
			Description: "Search memory for facts matching a query. Returns entities with their observations, sorted by relevance and confidence. Updates access counts (frequently recalled facts resist decay). Use compact: true for a lightweight first pass — content is truncated to the configured compact snippet length (120 chars by default) and truncated: true is set on each clipped observation. Fetch full content with get_observations({ observation_ids: [...] }).",
			Required:    []string{"query"},
			InputSchema: objectSchema(map[string]any{
				"query":   stringProperty("Free-text search query"),
				"limit":   numberProperty("Max results (default 20)"),
				"compact": map[string]any{"type": "boolean", "description": "If true, truncate observation content to the configured compact snippet length (120 chars by default). Use get_observations({ observation_ids: [...] }) to expand specific results."},
				"project": stringProperty("Project workspace path for project-scoped memory (absolute or relative to ~). Omit for global memory."),
			}, "query"),
		},
		{
			Name:        "recall_entity",
			Description: "Get everything known about a specific entity: all observations, all relations. Use when you know the exact entity name.",
			Required:    []string{"entity"},
			InputSchema: objectSchema(map[string]any{
				"entity":  stringProperty("Exact entity name"),
				"project": stringProperty("Project workspace path for project-scoped memory (absolute or relative to ~). Omit for global memory."),
			}, "entity"),
		},
		{
			Name:        "relate",
			Description: "Create a relation between two entities. Creates entities if they do not exist.",
			Required:    []string{"from", "to", "relation_type"},
			InputSchema: objectSchema(map[string]any{
				"from":          stringProperty("Source entity name"),
				"to":            stringProperty("Target entity name"),
				"relation_type": stringProperty(`Relation type (e.g. "works_with", "uses", "depends_on")`),
				"context":       stringProperty("Optional context for the relation"),
				"project":       stringProperty("Project workspace path for project-scoped memory (absolute or relative to ~). Omit for global memory."),
			}, "from", "to", "relation_type"),
		},
		{
			Name:        "forget",
			Description: "Remove a specific observation by ID, or an entire entity by name (cascading all observations and relations).",
			InputSchema: objectSchema(map[string]any{
				"observation_id": numberProperty("Specific observation ID to remove"),
				"entity":         stringProperty("Entity name to remove entirely (cascades)"),
				"project":        stringProperty("Project workspace path for project-scoped memory (absolute or relative to ~). Omit for global memory."),
			}),
		},
		{
			Name:        "list_entities",
			Description: "List all known entities, optionally filtered by type. Useful for orientation at session start.",
			InputSchema: objectSchema(map[string]any{
				"entity_type": stringProperty(`Filter by type (e.g. "person", "project")`),
				"limit":       numberProperty("Max results (default 50)"),
				"project":     stringProperty("Project workspace path for project-scoped memory (absolute or relative to ~). Omit for global memory."),
			}),
		},
		{
			Name:        "remember_event",
			Description: "Create an event (a session, meeting, decision, etc.) and optionally attach observations to it in one call. Events group related observations across entities so they can be recalled as a coherent block.",
			Required:    []string{"label"},
			InputSchema: objectSchema(map[string]any{
				"label":      stringProperty(`Event label (e.g. "Weekly standup", "Architecture decision", "Debugging session")`),
				"event_date": stringProperty("When the event happened (ISO8601 date or datetime, e.g. \"2025-04-01\")"),
				"event_type": stringProperty(`Event type (e.g. "meeting", "decision", "review", "session")`),
				"context":    stringProperty("Optional free-form context about the event"),
				"expires_at": stringProperty("Optional expiry datetime (ISO8601). Event auto-hides after this time."),
				"observations": map[string]any{
					"type":        "array",
					"description": "Optional array of observations to create and attach to this event",
					"items": objectSchema(map[string]any{
						"entity":      map[string]any{"type": "string"},
						"entity_type": map[string]any{"type": "string"},
						"observation": map[string]any{"type": "string"},
						"source":      map[string]any{"type": "string"},
						"confidence":  map[string]any{"type": "number"},
					}, "entity", "observation"),
				},
				"project": stringProperty("Project workspace path for project-scoped memory (absolute or relative to ~). Omit for global memory."),
			}, "label"),
		},
		{
			Name:        "recall_events",
			Description: "Search events by label, type, or date range. Returns events with observation counts. Use this to find sessions, meetings, or other grouped memory blocks.",
			InputSchema: objectSchema(map[string]any{
				"query":      stringProperty("Free-text search on event labels"),
				"event_type": stringProperty(`Filter by event type (e.g. "meeting", "decision")`),
				"date_from":  stringProperty("Start date filter (ISO8601)"),
				"date_to":    stringProperty("End date filter (ISO8601)"),
				"limit":      numberProperty("Max results (default 20)"),
				"project":    stringProperty("Project workspace path for project-scoped memory (absolute or relative to ~). Omit for global memory."),
			}),
		},
		{
			Name:        "recall_event",
			Description: "Get a specific event with all its observations grouped by entity. Use when you have an event ID from recall_events or remember_event.",
			Required:    []string{"event_id"},
			InputSchema: objectSchema(map[string]any{
				"event_id": numberProperty("Event ID to retrieve"),
				"project":  stringProperty("Project workspace path for project-scoped memory (absolute or relative to ~). Omit for global memory."),
			}, "event_id"),
		},
		{
			Name:        "get_observations",
			Description: "Fetch observations directly by observation ID. Use this to hydrate provenance from known results without running a new search.",
			Required:    []string{"observation_ids"},
			InputSchema: objectSchema(map[string]any{
				"observation_ids": map[string]any{
					"type":        "array",
					"uniqueItems": true,
					"description": "Positive integer observation IDs to fetch directly",
					"items": map[string]any{
						"type":    "integer",
						"minimum": 1,
					},
				},
				"project": stringProperty("Project workspace path for project-scoped memory (absolute or relative to ~). Omit for global memory."),
			}, "observation_ids"),
		},
		{
			Name:        "get_event_observations",
			Description: "Fetch raw observations for a known event ID without the grouped recall_event envelope.",
			Required:    []string{"event_id"},
			InputSchema: objectSchema(map[string]any{
				"event_id": numberProperty("Event ID to fetch observations for"),
				"project":  stringProperty("Project workspace path for project-scoped memory (absolute or relative to ~). Omit for global memory."),
			}, "event_id"),
		},
	}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func numberProperty(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}
