// Package tools is the Noetive MCP tool surface, for a server that is not the
// stdio binary in this repository.
//
// Each product is registered on its own, so a server serves exactly the
// products it names. Every product exposes the same four things: a backend
// interface the server implements, a Register function, its tool names, and the instructions an agent
// reads at initialize.
//
// # The credential is not this package's business
//
// Nothing here reads, holds or forwards a credential. The backend is called
// with the tool call's own context, unchanged, so a server that carries a
// per-request credential on that context and opens a client with it serves many
// callers without this package knowing there is more than one.
//
// # Targeting
//
// Registered tools apply no configured namespace, model or dimensions: every
// call must name its own. A server shared by many callers has no one to
// configure them for, and a default would route a caller's data somewhere they
// never named.
package tools

import (
	"context"
	"fmt"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"go.noetive.io/noetive-mcp/internal/broker"
	"go.noetive.io/noetive-mcp/internal/targeting"
)

// SemantikBackend is what the Semantik tools call. *semantik.Client satisfies
// it; a server that opens a client per request implements it by doing so.
type SemantikBackend interface {
	Publish(ctx context.Context, req semantik.PublishRequest) (semantik.PublishResponse, error)
	Search(ctx context.Context, req semantik.SearchRequest) (semantik.SearchResponse, error)
	Subscribe(ctx context.Context, req semantik.SubscribeRequest) (*semantik.Subscription, error)
	Lint(ctx context.Context, req semantik.LintRequest) (semantik.LintResponse, error)
	Health(ctx context.Context) error
}

// SemantikOptions tunes the Semantik tools to where they are served.
type SemantikOptions struct {
	// SubscribeBudget bounds how long one subscribe call may take, setup
	// included. Set it below the idle timeout of anything between the agent
	// and the server: a subscribe sends nothing until it reports, so a proxy
	// that cuts idle connections turns a long watch into a gateway error.
	// Zero means no such limit, which is setup plus a minute's window.
	//
	// It bounds subscribe only. The other tools keep their own fixed 30 second
	// per-call timeout, so a proxy that cuts idle connections sooner than that
	// is not made safe by this option alone.
	SubscribeBudget time.Duration
}

// RegisterSemantik adds the tools [SemantikToolNames] lists to srv.
//
// It refuses a SubscribeBudget too short to leave a subscribe any window at
// all: that is a wiring mistake, and would otherwise surface as a tool that
// can only fail.
func RegisterSemantik(srv *mcpserver.MCPServer, b SemantikBackend, opts SemantikOptions) error {
	if srv == nil || b == nil {
		return fmt.Errorf("tools: RegisterSemantik needs a server and a backend")
	}
	budget := opts.SubscribeBudget
	if budget == 0 {
		budget = broker.DefaultCallBudget
	}
	if budget < broker.MinCallBudget {
		return fmt.Errorf("tools: a SubscribeBudget of %s leaves a subscribe no window; the least that does is %s", budget, broker.MinCallBudget)
	}
	broker.Register(srv, b, targeting.Policy{}, budget)
	return nil
}

// SemantikToolNames lists the tools [RegisterSemantik] adds. tools/list returns
// tools sorted by name, so a server combining products gets the same tool array
// on every call whatever order it registers them in.
func SemantikToolNames() []string { return broker.ToolNames() }

// SemantikInstructions is what an agent should read about the Semantik tools
// at initialize, for a server that applies no configured target.
func SemantikInstructions() string { return broker.Instructions(targeting.Policy{}) }
