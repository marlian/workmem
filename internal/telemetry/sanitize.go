package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// encodeNoEscape marshals v as JSON without escaping <, >, & — telemetry
// rows are never rendered as HTML, and the escapes obscure sentinel markers
// like "<51 chars>" that must remain readable in the DB.
func encodeNoEscape(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// SanitizeArgs produces the args_summary string for a tool call.
//
// Rules:
//   - "observation", "content", "context" string values are replaced with
//     "<N chars>" (length only, never content)
//   - "facts" and "observations" arrays are replaced with "<N facts>" /
//     "<N observations>" (count only, never contents)
//   - "entity", "from", "to", "label", "query" string values are hashed when
//     strict is true; left unchanged otherwise
//   - all other fields pass through unchanged
func SanitizeArgs(args map[string]any, strict bool) string {
	if args == nil {
		return ""
	}
	safe := make(map[string]any, len(args))
	for k, v := range args {
		switch k {
		case "observation", "content", "context":
			if s, ok := v.(string); ok {
				safe[k] = fmt.Sprintf("<%d chars>", utf8.RuneCountInString(s))
				continue
			}
			safe[k] = v
		case "facts":
			if arr, ok := v.([]any); ok {
				safe[k] = fmt.Sprintf("<%d facts>", len(arr))
				continue
			}
			safe[k] = v
		case "observations":
			if arr, ok := v.([]any); ok {
				safe[k] = fmt.Sprintf("<%d observations>", len(arr))
				continue
			}
			safe[k] = v
		case "entity", "from", "to", "label", "query":
			if s, ok := v.(string); ok {
				safe[k] = hashIfStrict(s, strict)
				continue
			}
			safe[k] = v
		default:
			safe[k] = v
		}
	}
	out, err := encodeNoEscape(safe)
	if err != nil {
		return ""
	}
	return out
}

// Summarizable is implemented by result types that want to produce their
// own telemetry summary without going through the JSON round-trip fallback.
// This is the fast path: types returning large payloads (large recall
// results, big graph dumps) can implement this to avoid the cost of a
// full Marshal + Unmarshal on every tool call.
//
// The returned map must contain only count/boolean/short-string values —
// never the actual payload content. SummarizeResult serializes the map as
// JSON without any further inspection.
type Summarizable interface {
	TelemetrySummary() map[string]any
}

// SummarizeResult extracts count-only telemetry fields from a tool result.
// It never leaks content: only counts, booleans, and structural indicators.
//
// Fast path: if the result implements Summarizable, its TelemetrySummary()
// map is serialized directly. Fallback: JSON round-trip so the telemetry
// package stays domain-agnostic for types that do not opt in.
func SummarizeResult(result any) string {
	if result == nil {
		return ""
	}
	if s, ok := result.(Summarizable); ok {
		out, err := encodeNoEscape(s.TelemetrySummary())
		if err != nil {
			return ""
		}
		return out
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	out := make(map[string]any, 8)
	for _, k := range []string{"total_facts", "total", "stored", "deleted", "created", "found", "observations_attached", "compact"} {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	if arr, ok := m["results"].([]any); ok {
		out["entity_groups"] = len(arr)
	}
	if arr, ok := m["entities"].([]any); ok {
		out["entities"] = len(arr)
	}
	if arr, ok := m["events"].([]any); ok {
		out["events"] = len(arr)
	}
	if arr, ok := m["facts"].([]any); ok {
		out["facts"] = len(arr)
	}
	encoded, err := encodeNoEscape(out)
	if err != nil {
		return ""
	}
	return encoded
}
