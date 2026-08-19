package mcpserver_test

import (
	"context"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/mcpserver"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

const (
	initialize  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-editor","version":"1.0"}}}`
	initialized = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	listTools   = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
)

// The tool surface is the product: an editor sees exactly these names and
// nothing else. A tool added to broker but never registered is invisible, and a
// renamed tool silently breaks every agent that learned the old name.
func TestEveryToolIsRegisteredUnderItsPublishedName(t *testing.T) {
	srv := mcpserver.New("test", &stubBroker{}, targeting.Target{})

	srv.HandleMessage(context.Background(), json.RawMessage(initialize))
	srv.HandleMessage(context.Background(), json.RawMessage(initialized))
	resp := srv.HandleMessage(context.Background(), json.RawMessage(listTools))

	var envelope struct {
		Result struct {
			Tools []mcp.Tool `json:"tools"`
		} `json:"result"`
	}
	decode(t, resp, &envelope)

	registered := make(map[string]mcp.Tool, len(envelope.Result.Tools))
	for _, tool := range envelope.Result.Tools {
		registered[tool.Name] = tool
	}

	expected := mcpserver.ToolNames()
	if len(registered) != len(expected) {
		t.Fatalf("expected %d tools, got %d: %v", len(expected), len(registered), registered)
	}
	for _, name := range expected {
		if _, ok := registered[name]; !ok {
			t.Errorf("tool %q is not registered", name)
		}
	}
}

// A tool description is the only thing that tells a model when to reach for a
// tool. An empty one makes the tool dead weight in every editor.
func TestEveryToolDescribesItself(t *testing.T) {
	srv := mcpserver.New("test", &stubBroker{}, targeting.Target{})

	srv.HandleMessage(context.Background(), json.RawMessage(initialize))
	srv.HandleMessage(context.Background(), json.RawMessage(initialized))
	resp := srv.HandleMessage(context.Background(), json.RawMessage(listTools))

	var envelope struct {
		Result struct {
			Tools []mcp.Tool `json:"tools"`
		} `json:"result"`
	}
	decode(t, resp, &envelope)

	for _, tool := range envelope.Result.Tools {
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
	}
}

// The instructions are how an agent learns the routing triple is mandatory and
// what the shared namespace is provisioned with. Without them, a server started
// with no configuration — exactly how the Kiro deeplink launches it — leaves the
// agent unable to make a single successful call.
func TestInstructionsTellTheAgentHowToTarget(t *testing.T) {
	srv := mcpserver.New("test", &stubBroker{}, targeting.Target{})

	resp := srv.HandleMessage(context.Background(), json.RawMessage(initialize))

	var envelope struct {
		Result mcp.InitializeResult `json:"result"`
	}
	decode(t, resp, &envelope)

	for _, want := range []string{"namespace", "global", "Qwen3-Embedding-4B", "1024"} {
		if !strings.Contains(envelope.Result.Instructions, want) {
			t.Errorf("expected the instructions to mention %q", want)
		}
	}
}

// End-to-end through the assembled server: a tool call has to travel the real
// JSON-RPC path, not just the handler in isolation, or a registration mistake
// would go unnoticed.
func TestToolCallReachesTheBrokerThroughTheAssembledServer(t *testing.T) {
	stub := &stubBroker{}
	srv := mcpserver.New("test", stub, targeting.Target{})

	srv.HandleMessage(context.Background(), json.RawMessage(initialize))
	srv.HandleMessage(context.Background(), json.RawMessage(initialized))
	resp := srv.HandleMessage(context.Background(),
		json.RawMessage(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"noetive_health","arguments":{}}}`))

	var envelope struct {
		Result mcp.CallToolResult `json:"result"`
	}
	decode(t, resp, &envelope)

	if envelope.Result.IsError {
		t.Fatalf("expected a successful health call, got an error result")
	}
	if !stub.healthCalled {
		t.Error("the broker was never reached")
	}
}

// The server reports the version it was built with. A binary that misreports
// its version makes `doctor` unable to detect drift between the npm package and
// the binary it resolved.
func TestServerReportsItsVersion(t *testing.T) {
	srv := mcpserver.New("1.2.3", &stubBroker{}, targeting.Target{})

	resp := srv.HandleMessage(context.Background(), json.RawMessage(initialize))

	var envelope struct {
		Result mcp.InitializeResult `json:"result"`
	}
	decode(t, resp, &envelope)

	if envelope.Result.ServerInfo.Version != "1.2.3" {
		t.Errorf("expected version 1.2.3, got %q", envelope.Result.ServerInfo.Version)
	}
}

func decode(t *testing.T, msg mcp.JSONRPCMessage, target any) {
	t.Helper()

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("failed to unmarshal response %s: %v", data, err)
	}
}

// stubBroker satisfies the full Broker surface so the assembled server can be
// built without a live Semantik account.
type stubBroker struct {
	healthCalled bool
}

func (s *stubBroker) Publish(context.Context, semantik.PublishRequest) (semantik.PublishResponse, error) {
	return semantik.PublishResponse{}, nil
}

func (s *stubBroker) Search(context.Context, semantik.SearchRequest) (semantik.SearchResponse, error) {
	return semantik.SearchResponse{}, nil
}

func (s *stubBroker) Subscribe(context.Context, semantik.SubscribeRequest) (*semantik.Subscription, error) {
	return nil, &semantik.SubscribeSetupError{Err: &semantik.Error{Code: semantik.CodeUnavailable}}
}

func (s *stubBroker) Lint(context.Context, semantik.LintRequest) (semantik.LintResponse, error) {
	return semantik.LintResponse{Valid: true}, nil
}

func (s *stubBroker) Health(context.Context) error {
	s.healthCalled = true
	return nil
}

// The tool list is fixed for the lifetime of the process, so advertising
// listChanged would promise notifications this server never sends. A client
// that believes it will be told about changes has no reason to re-list, and one
// that subscribes to a notification that never arrives is waiting forever.
func TestTheServerDoesNotPromiseToolListNotifications(t *testing.T) {
	srv := mcpserver.New("test", &stubBroker{}, targeting.Target{})

	resp := srv.HandleMessage(context.Background(), json.RawMessage(initialize))

	var envelope struct {
		Result mcp.InitializeResult `json:"result"`
	}
	decode(t, resp, &envelope)

	if envelope.Result.Capabilities.Tools == nil {
		t.Fatal("expected the server to advertise tool support")
	}
	if envelope.Result.Capabilities.Tools.ListChanged {
		t.Error("the server advertised listChanged but never sends the notification")
	}
}
