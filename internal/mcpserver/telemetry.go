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
// to its absolute form. Returns "" in two cases: the caller passed no
// project, and the rare case where os.UserHomeDir fails. When the caller
// originally supplied a non-empty project, logToolCall still records
// db_scope="project" for that call — only the project_path column is
// written as NULL. The scope reflects the caller's intent, not the
// resolver's success.
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
// the telemetry client is nil / logging failed). The caller is expected to
// pass the pointer it captured from r.telemetry.Load() so this helper never
// re-reads the atomic field — that avoids any formal race against a
// concurrent Runtime.Close() that may have Swap(nil)'d the pointer after
// the defer fired.
func (r *Runtime) logToolCall(
	tele *telemetry.Client,
	toolName string,
	req *mcp.CallToolRequest,
	argObject map[string]any,
	result any,
	projectRaw string,
	isError bool,
	elapsed time.Duration,
) int64 {
	if tele == nil {
		return 0
	}
	dbScope := "global"
	projectPath := ""
	if projectRaw != "" {
		dbScope = "project"
		projectPath = resolveProjectPath(projectRaw)
	}
	return tele.LogToolCall(telemetry.ToolCallInput{
		Tool:              toolName,
		Client:            detectClient(req),
		DBScope:           dbScope,
		ProjectPath:       projectPath,
		DurationMs:        float64(elapsed) / float64(time.Millisecond),
		ArgsSummary:       telemetry.SanitizeArgs(argObject, tele.Strict()),
		ResultSummary:     telemetry.SummarizeResult(result),
		IsError:           isError,
		ConflictsSurfaced: conflictsSurfacedCount(result),
	})
}

// conflictsSurfacedCount returns the number of possible_conflicts hints
// attached to a remember result, or 0 for every other tool and for
// remember calls that produced no hints. Lives here rather than in
// telemetry/ because the knowledge that only RememberResult carries the
// field belongs to the dispatch layer, not to the sink.
func conflictsSurfacedCount(result any) int {
	remember, ok := result.(store.RememberResult)
	if !ok {
		return 0
	}
	return len(remember.PossibleConflicts)
}

// logSearchMetrics mirrors the recall search_metrics row. No-op when the
// captured telemetry client is nil or the parent tool_call id is 0. Same
// captured-pointer rationale as logToolCall.
func (r *Runtime) logSearchMetrics(tele *telemetry.Client, toolCallID int64, m *store.SearchMetrics) {
	if tele == nil || toolCallID == 0 || m == nil {
		return
	}
	tele.LogSearchMetrics(telemetry.SearchMetricsInput{
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
