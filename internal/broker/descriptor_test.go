package broker_test

import (
	"math"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// Annotations are part of the descriptor an editor reads, and they change what
// the editor does before a call is ever made: a tool not marked read-only can
// be gated behind a confirmation prompt, which turns a search an agent should
// make freely into one that interrupts the user.
func TestReadOnlyToolsAreAnnotatedAsSuch(t *testing.T) {
	searchTool, _ := broker.SearchTool(&stubBroker{}, complete)
	subscribeTool, _ := broker.SubscribeTool(&stubBroker{}, complete)
	lintTool, _ := broker.LintTool(&stubBroker{})
	healthTool, _ := broker.HealthTool(&stubBroker{})

	for _, tool := range []mcp.Tool{searchTool, subscribeTool, lintTool, healthTool} {
		t.Run(tool.Name, func(t *testing.T) {
			if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
				t.Errorf("%s reads but is not annotated read-only", tool.Name)
			}
		})
	}
}

// Publish writes. Annotating it read-only would tell an editor it is safe to
// call without asking, which is the opposite of true.
func TestPublishIsNotAnnotatedReadOnly(t *testing.T) {
	tool, _ := broker.PublishTool(&stubBroker{}, complete)

	if tool.Annotations.ReadOnlyHint != nil && *tool.Annotations.ReadOnlyHint {
		t.Error("publish writes but is annotated read-only")
	}
}

// Health contacts the broker and changes nothing, so repeating it is free. The
// hint is what lets an editor retry it without asking.
func TestHealthIsAnnotatedIdempotent(t *testing.T) {
	tool, _ := broker.HealthTool(&stubBroker{})

	if tool.Annotations.IdempotentHint == nil || !*tool.Annotations.IdempotentHint {
		t.Error("health is repeatable but is not annotated idempotent")
	}
}

// The largest dimensionality a model can declare must be accepted. Rejecting at
// the boundary refuses a legitimate model for being exactly as large as the
// wire format allows.
func TestTheLargestDimensionalityIsAccepted(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.SearchTool(stub, targeting.Target{})

	result := call(t, handler, map[string]any{
		"query":      "MATCH",
		"namespace":  "incidents",
		"model":      "model-a",
		"dimensions": float64(math.MaxUint16),
	})

	if result.IsError {
		t.Fatalf("expected %d dimensions to be accepted, got: %s", math.MaxUint16, text(t, result))
	}
	if stub.searchReq.Dimensions != math.MaxUint16 {
		t.Errorf("expected the value to reach the wire, got %d", stub.searchReq.Dimensions)
	}
}

// A negative dimensionality is nonsense that would wrap to a large positive
// number if it were cast rather than checked, sending a plausible-looking but
// wrong value to the server.
func TestANegativeDimensionalityIsRefused(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.SearchTool(stub, targeting.Target{})

	requireError(t, call(t, handler, map[string]any{
		"query":      "MATCH",
		"namespace":  "incidents",
		"model":      "model-a",
		"dimensions": float64(-1),
	}))

	if stub.searchReq.Query != "" {
		t.Fatal("the broker was called with a negative dimensionality")
	}
}

// A retry hint is only meaningful when the server sent one. Rendering "retry
// after 0s" for an error that carries no hint tells an agent to retry
// immediately on a failure that will not resolve.
func TestNoRetryHintIsShownWhenTheServerSentNone(t *testing.T) {
	_, handler := broker.SearchTool(&stubBroker{searchErr: &semantik.Error{
		Code:       semantik.CodeInvalidRequest,
		Message:    "malformed query",
		HTTPStatus: 400,
	}}, complete)

	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	if strings.Contains(message, "retry after") {
		t.Errorf("a retry hint was invented for an error that carried none: %s", message)
	}
}

// When the server does send a hint it has to survive, because pacing a retry is
// the whole reason it is sent.
func TestARetryHintSurvivesWhenTheServerSendsOne(t *testing.T) {
	_, handler := broker.SearchTool(&stubBroker{searchErr: &semantik.Error{
		Code:       semantik.CodeUnavailable,
		RetryAfter: 250_000_000, // 250ms
		HTTPStatus: 503,
	}}, complete)

	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	if !strings.Contains(message, "retry after 250ms") {
		t.Errorf("expected the server's retry hint, got: %s", message)
	}
}
