package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

// New creates an MCP server with the hello_world tool registered.
// healthURL is the endpoint the tool will GET to check API health.
func New(version, healthURL string) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("noetive-mcp", version)

	tool := mcp.NewTool("hello_world",
		mcp.WithDescription("Check Noetive API health"),
	)

	s.AddTool(tool, helloWorldHandler(healthURL))
	return s
}

func helloWorldHandler(healthURL string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to create request: %s", err)), nil
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("health check failed: %s", err)), nil
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to read response: %s", err)), nil
		}

		if resp.StatusCode != http.StatusOK {
			return mcp.NewToolResultError(
				fmt.Sprintf("health check returned status %d: %s", resp.StatusCode, string(body)),
			), nil
		}

		return mcp.NewToolResultText(string(body)), nil
	}
}
