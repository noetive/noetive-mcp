package broker_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// complete is a fully-specified fallback, standing in for an operator who
// configured every routing field at startup.
var complete = targeting.Target{Namespace: "incidents", Model: "model-a", Dimensions: 512}

// call invokes a handler with the given arguments, as the MCP server would.
func call(t *testing.T, handler mcpserver.ToolHandlerFunc, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	result, err := handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: args},
	})
	if err != nil {
		t.Fatalf("handler returned a protocol error: %v", err)
	}
	if result == nil {
		t.Fatal("handler returned no result")
	}
	return result
}

// text returns the concatenated text content of a result.
func text(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	var b strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// requireError asserts the call failed and returns the message the agent sees.
func requireError(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	if !result.IsError {
		t.Fatalf("expected an error result, got success: %s", text(t, result))
	}
	return text(t, result)
}

// requireSuccess asserts the call succeeded and returns its structured content.
func requireSuccess(t *testing.T, result *mcp.CallToolResult) any {
	t.Helper()

	if result.IsError {
		t.Fatalf("expected success, got error: %s", text(t, result))
	}
	return result.StructuredContent
}

// A tool must never return a Go error for a broker failure. Doing so surfaces
// as a protocol-level error the model cannot see, so it cannot self-correct;
// the failure has to arrive as an error *result* instead.
func TestBrokerFailuresAreResultsNotProtocolErrors(t *testing.T) {
	apiErr := &semantik.Error{Code: semantik.CodeInvalidRequest, Message: "bad query", HTTPStatus: 400}

	scenarios := []struct {
		name    string
		handler mcpserver.ToolHandlerFunc
		args    map[string]any
	}{
		{"publish", handlerOf(broker.PublishTool(&stubBroker{publishErr: apiErr}, complete)), map[string]any{"text": "hello"}},
		{"search", handlerOf(broker.SearchTool(&stubBroker{searchErr: apiErr}, complete)), map[string]any{"query": "MATCH"}},
		{"lint", handlerOf(broker.LintTool(&stubBroker{lintErr: apiErr})), map[string]any{"query": "MATCH"}},
		{"health", handlerOf(broker.HealthTool(&stubBroker{healthErr: apiErr})), map[string]any{}},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			result, err := sc.handler(context.Background(), mcp.CallToolRequest{
				Params: mcp.CallToolParams{Arguments: sc.args},
			})
			if err != nil {
				t.Fatalf("expected the failure as a result, got a protocol error: %v", err)
			}
			if !result.IsError {
				t.Fatal("expected IsError to be set")
			}
		})
	}
}

// Each field the server sent is a distinct debugging affordance: the code says
// what class of failure it is, the retry hint says whether to try again, and the
// request id is what a human quotes to support. Collapsing them makes a
// transient outage indistinguishable from a permanent misconfiguration.
func TestServerErrorFieldsReachTheAgent(t *testing.T) {
	apiErr := &semantik.Error{
		Code:       semantik.CodeUnavailable,
		Message:    "subscription setup did not complete within the budget",
		RequestID:  "req_01hz",
		RetryAfter: 250 * time.Millisecond,
		HTTPStatus: 503,
	}

	_, handler := broker.SearchTool(&stubBroker{searchErr: apiErr}, complete)
	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	for _, want := range []string{semantik.CodeUnavailable, "budget", "req_01hz", "250ms"} {
		if !strings.Contains(message, want) {
			t.Errorf("expected the message to carry %q, got: %s", want, message)
		}
	}
}

// A pre-flight rejection never reached the network. Saying so tells the agent
// the request is malformed rather than the service being down, which are
// opposite remediations.
func TestPreflightRejectionIsDistinguishedFromServerFailure(t *testing.T) {
	preflight := &semantik.Error{Code: semantik.CodeInvalidRequest, Message: "namespace is required"}

	_, handler := broker.SearchTool(&stubBroker{searchErr: preflight}, complete)
	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	if !strings.Contains(message, "before the request was sent") {
		t.Errorf("expected the message to mark this as pre-flight, got: %s", message)
	}
}

// Transport failures are not wrapped in *semantik.Error by the SDK. They still
// have to reach the agent rather than being swallowed into a bare "failed".
func TestTransportFailuresStillCarryTheirCause(t *testing.T) {
	_, handler := broker.HealthTool(&stubBroker{healthErr: context.DeadlineExceeded})
	message := requireError(t, call(t, handler, map[string]any{}))

	if !strings.Contains(message, context.DeadlineExceeded.Error()) {
		t.Errorf("expected the underlying cause in the message, got: %s", message)
	}
}

// handlerOf discards the descriptor so table entries stay readable.
func handlerOf(_ mcp.Tool, handler mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return handler
}

// stubBroker records what a tool asked for and returns what the test dictates.
// It implements every narrow broker interface so one double serves all tools.
//
// Field ordering: pointer-and-interface fields first, then values.
type stubBroker struct {
	publishReq semantik.PublishRequest
	searchReq  semantik.SearchRequest
	subReq     semantik.SubscribeRequest
	lintReq    semantik.LintRequest

	publishResp semantik.PublishResponse
	searchResp  semantik.SearchResponse
	lintResp    semantik.LintResponse
	stream      broker.Stream

	publishErr error
	searchErr  error
	subErr     error
	lintErr    error
	healthErr  error
}

func (s *stubBroker) Publish(_ context.Context, req semantik.PublishRequest) (semantik.PublishResponse, error) {
	s.publishReq = req
	return s.publishResp, s.publishErr
}

func (s *stubBroker) Search(_ context.Context, req semantik.SearchRequest) (semantik.SearchResponse, error) {
	s.searchReq = req
	return s.searchResp, s.searchErr
}

func (s *stubBroker) Subscribe(_ context.Context, req semantik.SubscribeRequest) (broker.Stream, error) {
	s.subReq = req
	return s.stream, s.subErr
}

func (s *stubBroker) Lint(_ context.Context, req semantik.LintRequest) (semantik.LintResponse, error) {
	s.lintReq = req
	return s.lintResp, s.lintErr
}

func (s *stubBroker) Health(context.Context) error { return s.healthErr }
