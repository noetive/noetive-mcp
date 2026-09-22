// Package mcpserver assembles the Noetive MCP server.
//
// It registers the tools that package broker exposes and states the server's
// operating rules for the agent. It makes no decisions of its own: what a tool
// accepts and what it returns belongs to the tool.
package mcpserver

import (
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/noetive/noetive-mcp/internal/broker"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// Broker is the set of Semantik operations the server exposes as tools.
// *semantik.Client satisfies it.
type Broker = broker.Broker

// New builds the MCP server with every Noetive tool registered.
//
// policy carries the routing fields an operator configured and the namespaces
// they closed; fields the fallback leaves unset must arrive on each tool call.
//
//	srv := mcpserver.New(version, client, configured)
//	mcpserver.ServeStdio(srv)
func New(version string, b Broker, policy targeting.Policy) *mcpserver.MCPServer {
	srv := mcpserver.NewMCPServer("noetive-mcp", version,
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithInstructions(broker.Instructions(policy)),
		mcpserver.WithRecovery(),
	)
	broker.Register(srv, b, policy, broker.DefaultCallBudget)
	return srv
}

// ToolNames lists every tool this server registers, in registration order.
// Exported so the doctor skill and the tests have one place to read the surface
// from rather than each keeping its own copy.
func ToolNames() []string { return broker.ToolNames() }
