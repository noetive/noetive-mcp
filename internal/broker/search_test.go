package broker_test

import (
	"strings"
	"testing"

	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// Search is the one tool whose value is the content it returns, so content and
// metadata must survive the trip from the SDK response into the tool result.
func TestSearchReturnsContentAndMetadata(t *testing.T) {
	stub := &stubBroker{searchResp: semantik.SearchResponse{Results: []semantik.ResultItem{{
		Metadata:  map[string]string{"topic": "payments"},
		Content:   "the gateway returns 202 before the write lands",
		MessageID: "msg_01hz",
		Namespace: "incidents",
		Score:     0.87,
	}}}}
	_, handler := broker.SearchTool(stub, complete)

	result := call(t, handler, map[string]any{"query": "MATCH DISTANCE(\"gateway\") WITHIN 0.4"})
	if requireSuccess(t, result) == nil {
		t.Fatal("expected structured content carrying the matches")
	}

	if !strings.Contains(text(t, result), "1 match") {
		t.Errorf("expected the count in the text fallback, got: %s", text(t, result))
	}
}

// An agent reading a blank result cannot tell "nothing matched" from "the call
// failed", so an empty result set has to say so in words.
func TestSearchSaysSoWhenNothingMatched(t *testing.T) {
	stub := &stubBroker{searchResp: semantik.SearchResponse{}}
	_, handler := broker.SearchTool(stub, complete)

	result := call(t, handler, map[string]any{"query": "MATCH"})
	requireSuccess(t, result)

	if !strings.Contains(text(t, result), "No matches") {
		t.Errorf("expected an explicit empty-result message, got: %s", text(t, result))
	}
}

// The same data-isolation boundary as publish: a search against an unnamed
// namespace would read someone else's space.
func TestSearchWithoutATargetNeverReachesTheBroker(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.SearchTool(stub, targeting.Target{})

	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	if !strings.Contains(message, "namespace") {
		t.Errorf("expected the error to name the missing field, got: %s", message)
	}
	if stub.searchReq.Query != "" {
		t.Fatal("the broker was called despite an unresolved target")
	}
}

func TestSearchForwardsTheLimit(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.SearchTool(stub, complete)

	call(t, handler, map[string]any{"query": "MATCH", "limit": float64(5)})

	if stub.searchReq.Limit != 5 {
		t.Errorf("expected limit 5 on the wire, got %d", stub.searchReq.Limit)
	}
}

// An omitted limit must stay zero so the query's own LIMIT clause governs.
// Substituting a default here would silently truncate a query that asked for
// more.
func TestOmittedLimitLeavesTheQueryInCharge(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.SearchTool(stub, complete)

	call(t, handler, map[string]any{"query": "MATCH DISTANCE(\"x\") WITHIN 0.4 LIMIT 50"})

	if stub.searchReq.Limit != 0 {
		t.Errorf("expected no limit to be sent, got %d", stub.searchReq.Limit)
	}
}

func TestSearchRefusesANegativeLimit(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.SearchTool(stub, complete)

	requireError(t, call(t, handler, map[string]any{"query": "MATCH", "limit": float64(-1)}))

	if stub.searchReq.Query != "" {
		t.Fatal("the broker was called with a negative limit")
	}
}
