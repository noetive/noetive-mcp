package broker_test

import (
	"strings"
	"testing"

	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// The routing triple an agent passes must reach the wire unchanged. If the
// configured fallback could override it, an agent could not publish anywhere
// but the operator's namespace.
func TestPublishRoutesToTheNamespaceTheCallNames(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.PublishTool(stub, complete)

	call(t, handler, map[string]any{
		"text":       "the gateway returns 202 before the write lands",
		"namespace":  "payments",
		"model":      "model-b",
		"dimensions": float64(1024),
	})

	if stub.publishReq.Namespace != "payments" {
		t.Errorf("expected namespace payments, got %q", stub.publishReq.Namespace)
	}
	if stub.publishReq.Model != "model-b" {
		t.Errorf("expected model model-b, got %q", stub.publishReq.Model)
	}
	if stub.publishReq.Dimensions != 1024 {
		t.Errorf("expected 1024 dimensions, got %d", stub.publishReq.Dimensions)
	}
}

// This is the data-isolation boundary. A call that names no namespace, against
// a server configured with none, must be refused before any bytes leave the
// process — never quietly routed to a shared space.
func TestPublishWithoutATargetNeverReachesTheBroker(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.PublishTool(stub, targeting.Target{})

	message := requireError(t, call(t, handler, map[string]any{"text": "hello"}))

	if !strings.Contains(message, "namespace") {
		t.Errorf("expected the error to name the missing field, got: %s", message)
	}
	if stub.publishReq.Namespace != "" || len(stub.publishReq.Items) != 0 {
		t.Fatal("the broker was called despite an unresolved target")
	}
}

// An operator who configured a namespace should not have to repeat it on every
// call; the fallback is a human's explicit choice, not a guess.
func TestPublishFallsBackToTheConfiguredTarget(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.PublishTool(stub, complete)

	call(t, handler, map[string]any{"text": "hello"})

	if stub.publishReq.Namespace != complete.Namespace {
		t.Errorf("expected the configured namespace %q, got %q", complete.Namespace, stub.publishReq.Namespace)
	}
}

// Empty text produces an embedding of nothing and a search hit that means
// nothing. Refusing it costs one publish; accepting it pollutes the namespace.
func TestPublishRefusesBlankText(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.PublishTool(stub, complete)

	requireError(t, call(t, handler, map[string]any{"text": "   \t\n "}))

	if len(stub.publishReq.Items) != 0 {
		t.Fatal("the broker was called with blank text")
	}
}

// Metadata is stored verbatim and returned on every search hit. Coercing a
// number to its string form would make the stored label disagree with what the
// agent believes it wrote, so a non-string value is refused and named.
func TestPublishRefusesNonStringMetadataAndNamesTheKey(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.PublishTool(stub, complete)

	message := requireError(t, call(t, handler, map[string]any{
		"text":     "hello",
		"metadata": map[string]any{"topic": "payments", "retries": float64(3)},
	}))

	if !strings.Contains(message, "retries") {
		t.Errorf("expected the offending key to be named, got: %s", message)
	}
	if len(stub.publishReq.Items) != 0 {
		t.Fatal("the broker was called with unusable metadata")
	}
}

func TestPublishPassesStringMetadataThrough(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.PublishTool(stub, complete)

	call(t, handler, map[string]any{
		"text":     "hello",
		"metadata": map[string]any{"topic": "payments"},
	})

	if stub.publishReq.Metadata["topic"] != "payments" {
		t.Errorf("expected metadata to reach the wire, got %v", stub.publishReq.Metadata)
	}
}

// An idempotency key is what makes a retry safe. Dropping it silently would
// turn a retried publish into a duplicate message.
func TestPublishForwardsTheIdempotencyKey(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.PublishTool(stub, complete)

	call(t, handler, map[string]any{"text": "hello", "idempotency_key": "session-0001"})

	if stub.publishReq.IdempotencyKey != "session-0001" {
		t.Errorf("expected the idempotency key to reach the wire, got %q", stub.publishReq.IdempotencyKey)
	}
}

// An unrecognised ack mode must fail here rather than on the wire, where it
// would come back as an opaque invalid_request the agent cannot attribute.
func TestPublishRefusesAnUnknownAckMode(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.PublishTool(stub, complete)

	requireError(t, call(t, handler, map[string]any{"text": "hello", "ack": "eventually"}))

	if len(stub.publishReq.Items) != 0 {
		t.Fatal("the broker was called with an invalid ack mode")
	}
}

// The server-assigned identifiers are the only way an agent can correlate what
// it wrote with what it later reads back, so they must survive as structured
// content rather than only as prose.
func TestPublishReturnsTheServerAssignedIdentifiers(t *testing.T) {
	stub := &stubBroker{publishResp: semantik.PublishResponse{MessageID: "msg_01hz", Epoch: 7, Seq: 42}}
	_, handler := broker.PublishTool(stub, complete)

	result := call(t, handler, map[string]any{"text": "hello"})
	requireSuccess(t, result)

	if !strings.Contains(text(t, result), "msg_01hz") {
		t.Errorf("expected the message id in the text fallback, got: %s", text(t, result))
	}
	if result.StructuredContent == nil {
		t.Error("expected structured content carrying the identifiers")
	}
}
