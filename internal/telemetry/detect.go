package telemetry

import "os"

// ClientInfo identifies the MCP client currently calling the server.
type ClientInfo struct {
	Name    string
	Version string
	Source  string // "protocol" | "env" | "none"
}

// DetectClient resolves the active client identity. Priority:
//  1. protocolName / protocolVersion from the MCP initialize handshake
//  2. environment variables set by known MCP clients
//  3. ClientInfo{Name: "unknown", Source: "none"}
func DetectClient(protocolName, protocolVersion string) ClientInfo {
	if protocolName != "" {
		return ClientInfo{Name: protocolName, Version: protocolVersion, Source: "protocol"}
	}
	if os.Getenv("KILO") != "" {
		return ClientInfo{Name: "kilo", Version: os.Getenv("KILOCODE_VERSION"), Source: "env"}
	}
	if os.Getenv("CLAUDE_CODE_SSE_PORT") != "" {
		return ClientInfo{Name: "claude-code", Source: "env"}
	}
	if os.Getenv("CURSOR_TRACE_ID") != "" {
		return ClientInfo{Name: "cursor", Source: "env"}
	}
	if os.Getenv("WINDSURF_EXTENSION_ID") != "" {
		return ClientInfo{Name: "windsurf", Source: "env"}
	}
	if os.Getenv("VSCODE_MCP_HTTP_PREFER") != "" {
		return ClientInfo{Name: "vscode-copilot", Source: "env"}
	}
	if os.Getenv("TERM_PROGRAM") == "vscode" {
		return ClientInfo{Name: "vscode-unknown", Source: "env"}
	}
	return ClientInfo{Name: "unknown", Source: "none"}
}
