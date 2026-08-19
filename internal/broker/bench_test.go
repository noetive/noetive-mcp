package broker_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
)

// The hot paths are the tool handlers: every agent turn that touches Noetive
// runs one, and each allocation here is per tool call in a long-lived process
// that an editor keeps open all day.
//
// Search dominates. It is the tool an agent reaches for most, and it is the only
// one whose result size scales with what the server returns rather than with
// what the caller sent — a 50-hit response is 50 structs to shape.

func benchRequest(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
}

func run(b *testing.B, handler mcpserver.ToolHandlerFunc, request mcp.CallToolRequest) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()

	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		result, err := handler(ctx, request)
		if err != nil || result == nil {
			b.Fatalf("handler failed: %v", err)
		}
	}
}

func BenchmarkSearch(b *testing.B) {
	for _, hits := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("hits=%d", hits), func(b *testing.B) {
			results := make([]semantik.ResultItem, hits)
			for i := range results {
				results[i] = semantik.ResultItem{
					Metadata:  map[string]string{"topic": "payments", "service": "reconciliation"},
					Content:   "the gateway returns 202 before the write lands, so the ledger and the topic disagree for a few seconds",
					MessageID: "msg_01hz2k3m4n5p6q7r8s9t0v1w2x",
					Namespace: "incidents",
					Score:     0.87,
				}
			}

			_, handler := broker.SearchTool(&stubBroker{searchResp: semantik.SearchResponse{Results: results}}, complete)
			run(b, handler, benchRequest(map[string]any{
				"query": `MATCH DISTANCE("payment reconciliation") WITHIN 0.4 LIMIT 50`,
			}))
		})
	}
}

func BenchmarkPublish(b *testing.B) {
	_, handler := broker.PublishTool(&stubBroker{
		publishResp: semantik.PublishResponse{MessageID: "msg_01hz", Epoch: 7, Seq: 42},
	}, complete)

	run(b, handler, benchRequest(map[string]any{
		"text":            "the gateway returns 202 before the write lands",
		"metadata":        map[string]any{"topic": "payments", "service": "reconciliation"},
		"idempotency_key": "session-0001",
		"ack":             string(semantik.AckDurable),
	}))
}

// The targeting triple is resolved on every publish, search and subscribe, so
// its cost is paid by every call regardless of what the tool then does.
func BenchmarkTargetingResolution(b *testing.B) {
	_, handler := broker.SearchTool(&stubBroker{}, complete)

	b.Run("from configuration", func(b *testing.B) {
		run(b, handler, benchRequest(map[string]any{"query": "MATCH"}))
	})

	b.Run("from the call", func(b *testing.B) {
		run(b, handler, benchRequest(map[string]any{
			"query":      "MATCH",
			"namespace":  "incidents",
			"model":      "Qwen3-Embedding-4B",
			"dimensions": float64(1024),
		}))
	})
}

// Failures are not rare in normal operation — a transient 503 from the broker is
// routine — so error shaping is a hot path too, not a cold one.
func BenchmarkErrorShaping(b *testing.B) {
	_, handler := broker.SearchTool(&stubBroker{searchErr: &semantik.Error{
		Code:       semantik.CodeUnavailable,
		Message:    "subscription setup did not complete within the budget",
		RequestID:  "req_01hz2k3m4n5p6q7r8s9t0v1w2x",
		HTTPStatus: 503,
	}}, complete)

	run(b, handler, benchRequest(map[string]any{"query": "MATCH"}))
}

func BenchmarkLint(b *testing.B) {
	_, handler := broker.LintTool(&stubBroker{lintResp: semantik.LintResponse{
		Valid:      true,
		Normalized: `MATCH DISTANCE("payment reconciliation") WITHIN 0.4 LIMIT 10`,
	}})

	run(b, handler, benchRequest(map[string]any{"query": `match distance("payment reconciliation") within 0.4`}))
}
