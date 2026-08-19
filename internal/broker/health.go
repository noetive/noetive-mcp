package broker

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// HealthChecker reports whether the broker is reachable and the API key works.
type HealthChecker interface {
	Health(ctx context.Context) error
}

// HealthTool builds the noetive_health tool and its handler.
//
// This is the cheapest end-to-end check available: it exercises DNS, TLS, the
// API key and the broker in one call, which is why the doctor skill uses it to
// tell "the server is not installed" apart from "the server is installed but
// cannot reach Noetive".
//
//	tool, handler := broker.HealthTool(client)
//	srv.AddTool(tool, handler)
func HealthTool(h HealthChecker) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	tool := mcp.NewTool("noetive_health",
		mcp.WithDescription("Check that this editor can reach Noetive Semantik and that its API key is accepted. Call this first when another Noetive tool fails, to tell a connection or credential problem apart from a problem with the query."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
	)

	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()

		if err := h.Health(ctx); err != nil {
			return failure("noetive_health", err), nil
		}
		return mcp.NewToolResultText("Noetive Semantik is reachable and the API key was accepted."), nil
	}

	return tool, handler
}
