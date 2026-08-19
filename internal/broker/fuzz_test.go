package broker_test

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/noetive/noetive-mcp/internal/broker"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// Tool arguments are the untrusted input of an MCP server: they are chosen by a
// model, not by a programmer, and arrive as arbitrary decoded JSON. A handler
// that panics takes the whole editor session down, so the invariant these fuzz
// targets defend is that every input produces a result rather than a crash.

func FuzzPublishArguments(f *testing.F) {
	f.Add("hello", "incidents", "model-a", 1024, "key-1", "durable")
	f.Add("", "", "", 0, "", "")
	f.Add("\x00\ufeff", "  ", "\n", -1, "\xff\xfe", "STORED")
	f.Add("text", "ns", "m", 65536, "k", "durable")

	f.Fuzz(func(t *testing.T, text, namespace, model string, dimensions int, key, ack string) {
		_, handler := broker.PublishTool(&stubBroker{}, targeting.Target{})
		mustNotPanic(t, handler, map[string]any{
			"text":            text,
			"namespace":       namespace,
			"model":           model,
			"dimensions":      float64(dimensions),
			"idempotency_key": key,
			"ack":             ack,
		})
	})
}

func FuzzSearchArguments(f *testing.F) {
	f.Add("MATCH DISTANCE(\"x\") WITHIN 0.4", "incidents", "model-a", 1024, 10)
	f.Add("", "", "", 0, 0)
	f.Add("\x00", "\ufeff", "\xff", -2147483648, -2147483648)
	f.Add("MATCH", "ns", "m", 2147483647, 2147483647)

	f.Fuzz(func(t *testing.T, query, namespace, model string, dimensions, limit int) {
		_, handler := broker.SearchTool(&stubBroker{}, targeting.Target{})
		mustNotPanic(t, handler, map[string]any{
			"query":      query,
			"namespace":  namespace,
			"model":      model,
			"dimensions": float64(dimensions),
			"limit":      float64(limit),
		})
	})
}

// Lint's cursor indexes into the query, so a mismatched pair is the obvious way
// to provoke an out-of-range read.
func FuzzLintArguments(f *testing.F) {
	f.Add("MATCH", 0)
	f.Add("", 1)
	f.Add("\xff\xfe\x00", -1)
	f.Add("MATCH DISTANCE(\"x\")", 2147483647)

	f.Fuzz(func(t *testing.T, query string, cursor int) {
		_, handler := broker.LintTool(&stubBroker{})
		mustNotPanic(t, handler, map[string]any{"query": query, "cursor": float64(cursor)})
	})
}

// Arguments may also arrive with the wrong JSON type entirely — a model can
// send a string where a number belongs, or an array where an object belongs.
func FuzzArgumentTypeConfusion(f *testing.F) {
	f.Add("text", "1024")
	f.Add("", "")
	f.Add("\x00", "[]")

	f.Fuzz(func(t *testing.T, text, dimensions string) {
		_, handler := broker.PublishTool(&stubBroker{}, targeting.Target{})
		mustNotPanic(t, handler, map[string]any{
			"text":       text,
			"dimensions": dimensions,       // string where a number belongs
			"metadata":   []any{text},      // array where an object belongs
			"namespace":  map[string]any{}, // object where a string belongs
		})
	})
}

// mustNotPanic asserts a handler survives arbitrary arguments and always yields
// a result. Either outcome is acceptable — a refusal is as valid as a success —
// but neither a panic nor a nil result is.
func mustNotPanic(t *testing.T, handler mcpserver.ToolHandlerFunc, args map[string]any) {
	t.Helper()

	result, err := handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: args},
	})
	if err != nil {
		t.Fatalf("handler returned a protocol error for %v: %v", args, err)
	}
	if result == nil {
		t.Fatalf("handler returned no result for %v", args)
	}
}
