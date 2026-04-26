package mcpserver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerListsToolsAndCallsBackend(t *testing.T) {
	runtime, err := New(Config{DBPath: t.TempDir() + "/memory.db"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	errCh := make(chan error, 1)
	go func() {
		errCh <- runtime.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("session.ListTools() error = %v", err)
	}
	if len(tools.Tools) != 12 {
		t.Fatalf("expected 12 tools, got %d", len(tools.Tools))
	}

	rememberResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "remember",
		Arguments: map[string]any{
			"entity":      "MCPServerTest",
			"entity_type": "test",
			"observation": "MCP bridge stores facts end to end",
		},
	})
	if err != nil {
		t.Fatalf("session.CallTool(remember) error = %v", err)
	}
	if rememberResult.IsError {
		t.Fatalf("remember unexpectedly returned an error: %#v", rememberResult)
	}

	recallResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "recall",
		Arguments: map[string]any{
			"query": "bridge",
			"limit": 5,
		},
	})
	if err != nil {
		t.Fatalf("session.CallTool(recall) error = %v", err)
	}
	if recallResult.IsError {
		t.Fatalf("recall unexpectedly returned an error: %#v", recallResult)
	}

	text, ok := recallResult.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", recallResult.Content[0])
	}
	if !strings.Contains(text.Text, "MCPServerTest") {
		t.Fatalf("recall payload did not include remembered entity: %s", text.Text)
	}

	missingArgResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remember",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("session.CallTool(remember missing args) error = %v", err)
	}
	if !missingArgResult.IsError {
		t.Fatalf("expected remember missing args to return a tool error")
	}
	missingText, ok := missingArgResult.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content for missing args, got %T", missingArgResult.Content[0])
	}
	if !strings.Contains(missingText.Text, "Missing required arguments") {
		t.Fatalf("unexpected missing-args payload: %s", missingText.Text)
	}

	emptyObservationResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "remember",
		Arguments: map[string]any{
			"entity":      "EmptyObservationEntity",
			"observation": "   ",
		},
	})
	if err != nil {
		t.Fatalf("session.CallTool(remember empty observation) error = %v", err)
	}
	if !emptyObservationResult.IsError {
		t.Fatalf("expected empty observation remember to return a tool error")
	}
	emptyText, ok := emptyObservationResult.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content for empty observation error, got %T", emptyObservationResult.Content[0])
	}
	if !strings.Contains(emptyText.Text, "observation must be non-empty") {
		t.Fatalf("unexpected empty-observation payload: %s", emptyText.Text)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("session.Close() error = %v", err)
	}
	cancel()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("runtime.Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime.Run() did not exit after client shutdown")
	}
}

func TestNewCreatesPrivateDatabaseDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not portable on Windows")
	}

	dbDir := filepath.Join(t.TempDir(), "state")
	rt, err := New(Config{DBPath: filepath.Join(dbDir, "memory.db")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()

	info, err := os.Stat(dbDir)
	if err != nil {
		t.Fatalf("stat db dir error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("db dir mode = %o, want 700", got)
	}

	dbInfo, err := os.Stat(filepath.Join(dbDir, "memory.db"))
	if err != nil {
		t.Fatalf("stat db file error = %v", err)
	}
	if got := dbInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("db file mode = %o, want 600", got)
	}
}

func TestNewDoesNotTightenExistingDatabaseDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not portable on Windows")
	}

	dbDir := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir existing db dir error = %v", err)
	}
	if err := os.Chmod(dbDir, 0o755); err != nil {
		t.Fatalf("chmod existing db dir error = %v", err)
	}

	rt, err := New(Config{DBPath: filepath.Join(dbDir, "memory.db")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()

	info, err := os.Stat(dbDir)
	if err != nil {
		t.Fatalf("stat db dir error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("existing db dir mode = %o, want preserved 755", got)
	}

	dbInfo, err := os.Stat(filepath.Join(dbDir, "memory.db"))
	if err != nil {
		t.Fatalf("stat db file error = %v", err)
	}
	if got := dbInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("db file mode = %o, want 600", got)
	}
}

func TestServerRememberSurfacesPossibleConflicts(t *testing.T) {
	runtime, err := New(Config{DBPath: t.TempDir() + "/memory.db"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	errCh := make(chan error, 1)
	go func() {
		errCh <- runtime.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "conflict-hint-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	defer session.Close()

	first, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "remember",
		Arguments: map[string]any{
			"entity":      "API",
			"observation": "rate limit is 100 per minute",
		},
	})
	if err != nil {
		t.Fatalf("session.CallTool(remember seed) error = %v", err)
	}
	if first.IsError {
		t.Fatalf("remember seed unexpectedly returned an error: %#v", first)
	}
	firstPayload := unmarshalToolText(t, first)
	if _, present := firstPayload["possible_conflicts"]; present {
		t.Fatalf("seed write returned possible_conflicts; expected the field to be omitted on first-ever observation: %v", firstPayload)
	}
	seedObsID, ok := firstPayload["observation_id"].(float64)
	if !ok {
		t.Fatalf("seed payload observation_id missing or not a number: %#v", firstPayload["observation_id"])
	}

	second, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "remember",
		Arguments: map[string]any{
			"entity":      "API",
			"observation": "rate limit is 200 per minute",
		},
	})
	if err != nil {
		t.Fatalf("session.CallTool(remember follow-up) error = %v", err)
	}
	if second.IsError {
		t.Fatalf("remember follow-up unexpectedly returned an error: %#v", second)
	}
	followPayload := unmarshalToolText(t, second)
	rawHints, present := followPayload["possible_conflicts"]
	if !present {
		t.Fatalf("follow-up write missing possible_conflicts on near-duplicate content: %v", followPayload)
	}
	hints, ok := rawHints.([]any)
	if !ok {
		t.Fatalf("possible_conflicts expected array, got %T: %v", rawHints, rawHints)
	}
	if len(hints) == 0 {
		t.Fatalf("possible_conflicts present but empty; expected at least one hint")
	}
	firstHint, ok := hints[0].(map[string]any)
	if !ok {
		t.Fatalf("possible_conflicts[0] expected object, got %T", hints[0])
	}
	for _, field := range []string{"observation_id", "similarity", "snippet"} {
		if _, has := firstHint[field]; !has {
			t.Fatalf("possible_conflicts[0] missing field %q: %v", field, firstHint)
		}
	}
	hintObsID, ok := firstHint["observation_id"].(float64)
	if !ok {
		t.Fatalf("hint observation_id not a number: %#v", firstHint["observation_id"])
	}
	if hintObsID != seedObsID {
		t.Fatalf("hint observation_id = %v, want seed %v", hintObsID, seedObsID)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("session.Close() error = %v", err)
	}
	cancel()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("runtime.Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime.Run() did not exit after client shutdown")
	}
}

func TestServerCommandTransportSmoke(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	command := exec.Command("go", "run", "./cmd/workmem", "-db", filepath.Join(t.TempDir(), "command-transport.db"))
	command.Dir = repoRoot

	client := mcp.NewClient(&mcp.Implementation{Name: "command-transport-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("client.Connect() over stdio error = %v", err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "remember",
		Arguments: map[string]any{
			"entity":      "CommandTransportEntity",
			"observation": "stdio transport works",
		},
	})
	if err != nil {
		t.Fatalf("session.CallTool(remember) over stdio error = %v", err)
	}
	if result.IsError {
		t.Fatalf("remember over stdio unexpectedly returned an error: %#v", result)
	}

	recall, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "recall",
		Arguments: map[string]any{
			"query": "stdio",
		},
	})
	if err != nil {
		t.Fatalf("session.CallTool(recall) over stdio error = %v", err)
	}
	if recall.IsError {
		t.Fatalf("recall over stdio unexpectedly returned an error: %#v", recall)
	}

	text, ok := recall.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content over stdio, got %T", recall.Content[0])
	}
	if !strings.Contains(text.Text, "CommandTransportEntity") {
		t.Fatalf("stdio recall payload did not include remembered entity: %s", text.Text)
	}
}
