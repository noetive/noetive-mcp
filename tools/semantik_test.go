package tools_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/mark3labs/mcp-go/mcp"
	mcpgo "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"go.noetive.io/noetive-mcp/internal/mcpserver"
	"go.noetive.io/noetive-mcp/tools"
)

const (
	initialize  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	initialized = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	listTools   = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	callHealth  = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"noetive_health","arguments":{}}}`
)

// A server built on this package and the stdio binary must offer the same
// Semantik tools under the same names: an agent that learned one has learned the
// other. tools/list is sorted by name, so the listing is stable whatever order
// the tools were registered in.
func TestTheLibraryServesTheToolsTheStdioServerServes(t *testing.T) {
	srv := server(t, tools.SemantikOptions{})

	if got := listedNames(t, srv); !slices.Equal(got, slices.Sorted(slices.Values(tools.SemantikToolNames()))) {
		t.Errorf("registered %v, expected %v", got, tools.SemantikToolNames())
	}
	if !slices.Equal(tools.SemantikToolNames(), mcpserver.ToolNames()) {
		t.Errorf("library lists %v, stdio lists %v", tools.SemantikToolNames(), mcpserver.ToolNames())
	}
}

type credentialKey struct{}

// The call's context is the only channel a hosted server has to carry a
// per-request credential to the backend. If a tool swapped it for a fresh one,
// every call would reach the backend anonymous.
func TestTheBackendIsCalledWithTheToolCallsOwnContext(t *testing.T) {
	backend := &recordingBackend{}
	srv := mcpgo.NewMCPServer("test", "1")
	if err := tools.RegisterSemantik(srv, backend, tools.SemantikOptions{}); err != nil {
		t.Fatal(err)
	}

	ctx := context.WithValue(context.Background(), credentialKey{}, "keya_example")
	srv.HandleMessage(ctx, json.RawMessage(initialize))
	srv.HandleMessage(ctx, json.RawMessage(initialized))
	srv.HandleMessage(ctx, json.RawMessage(callHealth))

	if backend.seen != "keya_example" {
		t.Errorf("the backend saw %q on its context, expected the caller's value", backend.seen)
	}
}

// SubscribeBudget exists to keep a subscribe under a proxy's idle timeout. The window
// an agent is offered has to shrink with it, or the schema invites a request
// the proxy will cut off.
func TestSubscribeBudgetShrinksTheSubscribeWindowOffered(t *testing.T) {
	srv := server(t, tools.SemantikOptions{SubscribeBudget: 45 * time.Second})

	subscribe := listed(t, srv)["noetive_subscribe"]
	wait, ok := subscribe.InputSchema.Properties["wait_seconds"].(map[string]any)
	if !ok {
		t.Fatalf("noetive_subscribe has no wait_seconds property")
	}
	if got := wait["maximum"]; got != float64(25) {
		t.Errorf("wait_seconds maximum is %v, expected 25 (45s less the 20s setup budget)", got)
	}
	if !strings.Contains(subscribe.Description, "up to 25 seconds") {
		t.Errorf("the description still promises a longer window: %s", subscribe.Description)
	}
}

// A budget that cannot fit a subscribe is refused before anything is
// registered, so a wiring mistake stops the server rather than shipping a tool
// that can only fail.
func TestASubscribeBudgetTooShortForAnyWindowIsRefused(t *testing.T) {
	srv := mcpgo.NewMCPServer("test", "1")
	if err := tools.RegisterSemantik(srv, &recordingBackend{}, tools.SemantikOptions{SubscribeBudget: 5 * time.Second}); err == nil {
		t.Fatal("expected a SubscribeBudget shorter than the setup budget to be refused")
	}
	if got := listedNames(t, srv); len(got) != 0 {
		t.Errorf("a refused registration still added %v", got)
	}
}

// A shared server has no one to configure a target for, so its instructions
// must say every call names its own, and must not claim one was configured.
func TestInstructionsTellTheAgentToNameATarget(t *testing.T) {
	got := tools.SemantikInstructions()
	if !strings.Contains(got, "pass them on each call") {
		t.Errorf("instructions do not tell the agent to name its target: %s", got)
	}
}

func server(t *testing.T, opts tools.SemantikOptions) *mcpgo.MCPServer {
	t.Helper()
	srv := mcpgo.NewMCPServer("test", "1")
	if err := tools.RegisterSemantik(srv, &recordingBackend{}, opts); err != nil {
		t.Fatal(err)
	}
	return srv
}

func listed(t *testing.T, srv *mcpgo.MCPServer) map[string]mcp.Tool {
	t.Helper()
	byName := map[string]mcp.Tool{}
	for _, tool := range list(t, srv) {
		byName[tool.Name] = tool
	}
	return byName
}

func listedNames(t *testing.T, srv *mcpgo.MCPServer) []string {
	t.Helper()
	var names []string
	for _, tool := range list(t, srv) {
		names = append(names, tool.Name)
	}
	return names
}

func list(t *testing.T, srv *mcpgo.MCPServer) []mcp.Tool {
	t.Helper()
	ctx := context.Background()
	srv.HandleMessage(ctx, json.RawMessage(initialize))
	srv.HandleMessage(ctx, json.RawMessage(initialized))
	resp := srv.HandleMessage(ctx, json.RawMessage(listTools))

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result struct {
			Tools []mcp.Tool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decoding %s: %v", data, err)
	}
	return envelope.Result.Tools
}

// recordingBackend answers every call and remembers what Health saw on its
// context.
type recordingBackend struct {
	seen any
}

func (b *recordingBackend) Publish(context.Context, semantik.PublishRequest) (semantik.PublishResponse, error) {
	return semantik.PublishResponse{}, nil
}

func (b *recordingBackend) Search(context.Context, semantik.SearchRequest) (semantik.SearchResponse, error) {
	return semantik.SearchResponse{}, nil
}

func (b *recordingBackend) Subscribe(context.Context, semantik.SubscribeRequest) (*semantik.Subscription, error) {
	return nil, context.Canceled
}

func (b *recordingBackend) Lint(context.Context, semantik.LintRequest) (semantik.LintResponse, error) {
	return semantik.LintResponse{}, nil
}

func (b *recordingBackend) Health(ctx context.Context) error {
	b.seen = ctx.Value(credentialKey{})
	return nil
}
