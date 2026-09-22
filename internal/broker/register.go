package broker

import (
	"context"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/targeting"
)

// Broker is the set of Semantik operations the tools call. *semantik.Client
// satisfies it, and so does anything that opens a client per call.
type Broker interface {
	Publisher
	Searcher
	Linter
	HealthChecker

	Subscribe(ctx context.Context, req semantik.SubscribeRequest) (*semantik.Subscription, error)
}

// Register adds every Semantik tool to srv.
//
// One registration shared by every deployment, so a server that serves these
// tools cannot serve a different set of them. callBudget bounds a subscribe
// call; see [SubscribeToolWithin].
func Register(srv *mcpserver.MCPServer, b Broker, policy targeting.Policy, callBudget time.Duration) {
	srv.AddTool(PublishTool(b, policy))
	srv.AddTool(SearchTool(b, policy))
	srv.AddTool(SubscribeToolWithin(SubscriberFrom(b), policy, callBudget))
	srv.AddTool(LintTool(b))
	srv.AddTool(HealthTool(b))
}

// DefaultCallBudget is the longest a subscribe call may take when nothing
// between the agent and the server limits it: setup plus a minute's window.
const DefaultCallBudget = setupBudget + maxWait

// ToolNames lists every tool [Register] adds.
func ToolNames() []string {
	return []string{
		"noetive_publish",
		"noetive_search",
		"noetive_subscribe",
		"noetive_lint",
		"noetive_health",
	}
}

// Instructions tells the agent the two things it cannot infer from the tool
// schemas: that the routing triple is mandatory with no default, and what the
// shared namespace is actually provisioned with. Naming the concrete values
// here is what lets an agent call a tool successfully on a server started with
// no configuration, which is exactly how the Kiro deeplink launches it.
//
// An operator who closed the shared namespace gets the opposite sentence. The
// instructions are the first thing an agent reads and the strongest suggestion
// it receives, so advertising a namespace every call will be refused for would
// spend the agent's first attempt on a destination it cannot use.
func Instructions(policy targeting.Policy) string {
	shared := `The shared namespace is "global", provisioned with model "Qwen3-Embedding-4B" at 1024 dimensions.`
	if policy.GlobalDisabled {
		shared = `The shared "global" namespace is closed on this server: name the namespace your work belongs in, and never fall back to a shared one.`
	}

	return `Noetive Semantik is a semantic broker: agents publish messages and find each other's messages by meaning rather than by topic name.

Publish what a peer would want to find later, such as a conclusion, a root cause or a decision, and search before rediscovering something a peer may already have written.
Every publish, search and subscribe must name a namespace, an embedding model and its dimensions. There is no default. If this server was started without them configured, pass them on each call. ` + shared + `

Queries use SemQL. When a query is unfamiliar or a search reports invalid_request, check it with noetive_lint before retrying.`
}

// MinCallBudget is the shortest call budget that leaves a subscribe a window:
// the setup budget plus one second.
const MinCallBudget = setupBudget + time.Second
