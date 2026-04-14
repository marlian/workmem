package mcpserver

import (
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"workmem/internal/store"
	"workmem/internal/telemetry"
)

// detectClient reads clientInfo from the MCP initialize handshake when
// available, falling back to environment-variable detection otherwise.
func detectClient(req *mcp.CallToolRequest) telemetry.ClientInfo {
	var name, version string
	if req != nil && req.Session != nil {
		if params := req.Session.InitializeParams(); params != nil && params.ClientInfo != nil {
			name = params.ClientInfo.Name
			version = params.ClientInfo.Version
		}
	}
	return telemetry.DetectClient(name, version)
}

// resolveProjectPath resolves a project argument as provided by the caller
// to its absolute form. Returns "" when project is empty or unresolvable
// (including the rare case where os.UserHomeDir fails) — telemetry accepts
// empty and simply records a global-scope call without a path.
func resolveProjectPath(project string) string {
	if project == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return store.ResolveProjectPath(project, home)
}

// logToolCall inserts a tool_calls row and returns the insert id (or 0 when
// telemetry is disabled / logging failed). Safe to call when c is nil.
func (r *Runtime) logToolCall(
	toolName string,
	req *mcp.CallToolRequest,
	argObject map[string]any,
	result any,
	projectRaw string,
	isError bool,
	elapsed time.Duration,
) int64 {
	if r.telemetry == nil {
		return 0
	}
	dbScope := "global"
	projectPath := ""
	if projectRaw != "" {
		dbScope = "project"
		projectPath = resolveProjectPath(projectRaw)
	}
	return r.telemetry.LogToolCall(telemetry.ToolCallInput{
		Tool:          toolName,
		Client:        detectClient(req),
		DBScope:       dbScope,
		ProjectPath:   projectPath,
		DurationMs:    float64(elapsed) / float64(time.Millisecond),
		ArgsSummary:   telemetry.SanitizeArgs(argObject, r.telemetry.Strict()),
		ResultSummary: telemetry.SummarizeResult(result),
		IsError:       isError,
	})
}

// logSearchMetrics mirrors the recall search_metrics row. No-op when telemetry
// is disabled or the parent tool_call id is 0.
func (r *Runtime) logSearchMetrics(toolCallID int64, m *store.SearchMetrics) {
	if r.telemetry == nil || toolCallID == 0 || m == nil {
		return
	}
	r.telemetry.LogSearchMetrics(telemetry.SearchMetricsInput{
		ToolCallID:      toolCallID,
		Query:           m.Query,
		Channels:        m.Channels,
		CandidatesTotal: m.CandidatesTotal,
		ResultsReturned: m.ResultsReturned,
		LimitRequested:  m.LimitRequested,
		ScoreMin:        m.ScoreMin,
		ScoreMax:        m.ScoreMax,
		ScoreMedian:     m.ScoreMedian,
		Compact:         m.Compact,
	})
}
