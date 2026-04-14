package mcpserver

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
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
