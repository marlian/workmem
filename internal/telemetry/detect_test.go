package telemetry

import (
	"testing"
)

func TestDetectClientProtocolWins(t *testing.T) {
	t.Setenv("KILO", "1")
	info := DetectClient("claude-desktop", "0.9.1")
	if info.Name != "claude-desktop" || info.Version != "0.9.1" || info.Source != "protocol" {
		t.Fatalf("protocol should win, got %+v", info)
	}
}

func TestDetectClientEnvFallbackKilo(t *testing.T) {
	t.Setenv("KILO", "1")
	t.Setenv("KILOCODE_VERSION", "0.43.6")
	unsetMCPClientEnv(t)
	t.Setenv("KILO", "1")
	t.Setenv("KILOCODE_VERSION", "0.43.6")
	info := DetectClient("", "")
	if info.Name != "kilo" || info.Version != "0.43.6" || info.Source != "env" {
		t.Fatalf("kilo env not detected: %+v", info)
	}
}

func TestDetectClientEnvFallbackClaudeCode(t *testing.T) {
	unsetMCPClientEnv(t)
	t.Setenv("CLAUDE_CODE_SSE_PORT", "7123")
	info := DetectClient("", "")
	if info.Name != "claude-code" || info.Source != "env" {
		t.Fatalf("claude-code not detected: %+v", info)
	}
}

func TestDetectClientVSCodeCopilotRequiresNonEmptyVar(t *testing.T) {
	unsetMCPClientEnv(t)
	t.Setenv("VSCODE_MCP_HTTP_PREFER", "auto")
	info := DetectClient("", "")
	if info.Name != "vscode-copilot" || info.Source != "env" {
		t.Fatalf("vscode-copilot non-empty var not detected: %+v", info)
	}
}

func TestDetectClientNoSignals(t *testing.T) {
	unsetMCPClientEnv(t)
	info := DetectClient("", "")
	if info.Name != "unknown" || info.Source != "none" {
		t.Fatalf("no-signal detection wrong: %+v", info)
	}
}

func unsetMCPClientEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{
		"KILO", "KILOCODE_VERSION",
		"CLAUDE_CODE_SSE_PORT",
		"CURSOR_TRACE_ID",
		"WINDSURF_EXTENSION_ID",
		"VSCODE_MCP_HTTP_PREFER",
		"TERM_PROGRAM",
	} {
		t.Setenv(v, "")
	}
	// t.Setenv with "" sets to empty, but LookupEnv still reports present.
	// For the ones we need genuinely absent, unset via os.Unsetenv is not
	// t.Setenv-compatible; we accept that "" is how the tests model absence
	// for signals whose presence-only matters (VSCODE_MCP_HTTP_PREFER handled
	// in its own test).
}
