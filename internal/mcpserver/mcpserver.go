// Package mcpserver assembles the Noetive MCP server.
//
// It registers the tools that package broker exposes and states the server's
// operating rules for the agent. It makes no decisions of its own: what a tool
// accepts and what it returns belongs to the tool.
package mcpserver

import (
	"context"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// instructions tell the agent the two things it cannot infer from the tool
// schemas: that the routing triple is mandatory with no default, and what the
// shared namespace is actually provisioned with. Naming the concrete values
// here is what lets an agent call a tool successfully on a server started with
// no configuration, which is exactly how the Kiro deeplink launches it.
const instructions = `Noetive Semantik is a semantic broker: agents publish messages and find each other's messages by meaning rather than by topic name.

Publish what a peer would want to find later — a conclusion, a root cause, a decision — and search before rediscovering something a peer may already have written.

Every publish, search and subscribe must name a namespace, an embedding model and its dimensions. There is no default. If this server was started without them configured, pass them on each call. The shared namespace is "global", provisioned with model "Qwen3-Embedding-4B" at 1024 dimensions.

Queries use SemQL. When a query is unfamiliar or a search reports invalid_request, check it with noetive_lint before retrying.`

// Broker is the set of Semantik operations the server exposes as tools.
// *semantik.Client satisfies it.
type Broker interface {
	broker.Publisher
	broker.Searcher
	broker.Linter
	broker.HealthChecker

	Subscribe(ctx context.Context, req semantik.SubscribeRequest) (*semantik.Subscription, error)
}

// New builds the MCP server with every Noetive tool registered.
//
// fallback carries the routing fields an operator configured; fields it leaves
// unset must arrive on each tool call.
//
//	srv := mcpserver.New(version, client, configured)
//	mcpserver.ServeStdio(srv)
func New(version string, b Broker, fallback targeting.Target) *mcpserver.MCPServer {
	srv := mcpserver.NewMCPServer("noetive-mcp", version,
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithInstructions(instructions),
		mcpserver.WithRecovery(),
	)

	srv.AddTool(broker.PublishTool(b, fallback))
	srv.AddTool(broker.SearchTool(b, fallback))
	srv.AddTool(broker.SubscribeTool(broker.SubscriberFrom(b), fallback))
	srv.AddTool(broker.LintTool(b))
	srv.AddTool(broker.HealthTool(b))

	return srv
}

// ToolNames lists every tool this server registers, in registration order.
// Exported so the doctor skill and the tests have one place to read the surface
// from rather than each keeping its own copy.
func ToolNames() []string {
	return []string{
		"noetive_publish",
		"noetive_search",
		"noetive_subscribe",
		"noetive_lint",
		"noetive_health",
	}
}
