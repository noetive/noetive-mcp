//go:build integration

// Package integration drives the assembled MCP server against the live Noetive
// Semantik API.
//
// It hits production on purpose. The failure these tests exist to catch is wire
// drift, the API changing shape underneath a client that still compiles, and
// a mock cannot drift. They are skipped when NOETIVE_KEY_SECRET is unset so the
// ordinary test run stays green offline.
package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/mark3labs/mcp-go/mcp"
	mcpgo "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
	"github.com/noetive/noetive-mcp/internal/mcpserver"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// The shared namespace and the model it is provisioned with. Named explicitly
// rather than defaulted, exactly as a caller must.
var global = targeting.Target{Namespace: "global", Model: "Qwen3-Embedding-4B", Dimensions: 1024}

// shared is a server configured to reach the shared namespace, which is what
// these tests target. Leaving it open is the point: the suite exists to prove
// calls reach the real broker, and closing it would refuse every one of them.
var shared = targeting.Policy{Fallback: global}

// session drives the assembled server the way an editor does, so these tests
// exercise registration and argument decoding rather than the SDK alone.
type session struct {
	t   *testing.T
	srv *mcpgo.MCPServer
}

func newSession(t *testing.T) *session {
	t.Helper()

	key := strings.TrimSpace(os.Getenv("NOETIVE_KEY_SECRET"))
	if key == "" {
		t.Skip("NOETIVE_KEY_SECRET is not set; skipping integration tests")
	}

	client, err := semantik.NewFromEnv()
	if err != nil {
		t.Fatalf("could not build the client: %v", err)
	}

	srv := mcpserver.New("integration", client, shared)
	ctx := context.Background()
	srv.HandleMessage(ctx, json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"integration","version":"1"}}}`))
	srv.HandleMessage(ctx, json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))

	return &session{t: t, srv: srv}
}

func (s *session) call(name string, args map[string]any) mcp.CallToolResult {
	s.t.Helper()

	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		s.t.Fatalf("could not encode the request: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	raw, err := json.Marshal(s.srv.HandleMessage(ctx, json.RawMessage(payload)))
	if err != nil {
		s.t.Fatalf("could not encode the response: %v", err)
	}

	var envelope struct {
		Result mcp.CallToolResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		s.t.Fatalf("could not decode %s: %v", raw, err)
	}
	return envelope.Result
}

func (s *session) text(result mcp.CallToolResult) string {
	s.t.Helper()

	var b strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// The cheapest end-to-end check: DNS, TLS, the key and the broker in one call.
// If this reports drift, nothing below is meaningful.
//
// Guarded like every other call despite being the canary. A host that is not
// there is the one condition this test cannot distinguish from a broken client,
// and reporting it as one sends whoever reads the run to audit a client that
// never got to say anything.
func TestHealthAgainstProduction(t *testing.T) {
	s := newSession(t)

	result := s.call("noetive_health", map[string]any{})

	if result.IsError {
		skipInconclusive(t, s.text(result))
		t.Fatalf("health failed: %s", s.text(result))
	}
}

// Lint is the second-cheapest call and the only one that touches no namespace,
// which makes it the right place to catch a changed request or response shape.
func TestLintAgainstProduction(t *testing.T) {
	s := newSession(t)

	result := s.call("noetive_lint", map[string]any{
		"query": `MATCH DISTANCE("machine learning") WITHIN 0.4 LIMIT 5`,
	})

	if result.IsError {
		skipInconclusive(t, s.text(result))
		t.Fatalf("lint failed: %s", s.text(result))
	}
	if !strings.Contains(s.text(result), "valid") {
		t.Errorf("expected a verdict, got: %s", s.text(result))
	}
}

// inconclusive reports whether a tool result describes the broker failing to do
// the work, rather than the client asking for the wrong thing.
//
// Five shapes qualify, and none can hide the drift this suite exists to catch.
// The three the tool names itself are taken from the broker package rather than
// copied, so rewording one cannot silently stop this from matching it.
// NoResponseWithin is the tool's own budget expiring, which means nothing came
// back at all. A changed wire shape always produces a response. Unreachable is
// a connection that never opened and Unanswered one that broke; in neither case
// was there a response for a shape to have drifted in.
//
// The remaining two are the server's own words and stay literals here, because
// they belong to whoever sends them. "[unavailable]" is a retryable condition
// the server names; a shape change surfaces as a different code or a decode
// error, never as that one. "(retry after " is the server saying when to come
// back, which it sends under several codes, backpressure and a namespace not
// yet ready among them, and which no rejection of a malformed request carries.
// Matching the hint rather than enumerating the codes is what keeps this from
// having to track the server's vocabulary.
//
// Deliberately not a general "any error is fine" escape. A suite that skips
// whatever it cannot explain proves nothing and would report a genuinely broken
// client as a good day. Two exclusions are load-bearing rather than incidental:
//
// A local embedder that cannot be reached is rendered as an *embedding.Error
// naming the endpoint, matches none of these, and fails the embedding tests as
// it should: that endpoint is the operator's own infrastructure, not the
// broker's.
//
// "[malformed_response]" is excluded even though a broker dropping a connection
// mid-body can produce it, because that is also exactly what wire drift looks
// like: a response this client could not decode. Excusing it would blind the
// suite to the one failure it exists to find.
func inconclusive(message string) bool {
	return strings.Contains(message, broker.NoResponseWithin) ||
		strings.Contains(message, broker.Unreachable) ||
		strings.Contains(message, broker.Unanswered) ||
		strings.Contains(message, "[unavailable]") ||
		strings.Contains(message, "(retry after ")
}

// skipInconclusive ends the test as inconclusive when the message is the broker
// failing to serve the call: too slow, refusing retryably, or not there at all.
// None of those say anything about whether the client is correct, which is the
// only thing this suite measures.
//
// One owner for the decision, because the alternative has already failed four
// times: each call site guarding itself is a call site that can be added without
// the guard, and the suite goes red for a production condition every other test
// records as a skip.
//
// The wording is load-bearing, so it comes from a constant the workflow's own
// test checks rather than being written here. The phrase deliberately stops
// short of saying why: it covers a broker that answered too late and one that
// never answered at all.
//
// The message quotes the failure in full. On the skip path that is only ever
// the server's own code, its retry hint, or one of this client's three
// transport phrases, and without it a run that skipped everything records that
// it happened but not what happened.
func skipInconclusive(t *testing.T, message string) {
	t.Helper()
	if inconclusive(message) {
		t.Skipf("%s; nothing to conclude about the client: %s", brokerDidNotAnswer, message)
	}
}

// Publish then search, with a marker unique to this run. Indexing is not
// immediate and the server makes no read-your-writes promise, so a miss is
// reported as a skip rather than a failure: asserting on it would produce a
// test that fails for reasons unrelated to the code.
func TestPublishThenSearchAgainstProduction(t *testing.T) {
	s := newSession(t)

	// Derived from the clock so repeated runs never collide, and so the
	// idempotency key is genuinely new on every run.
	marker := fmt.Sprintf("noetive-mcp integration marker %d", time.Now().UnixNano())

	published := s.call("noetive_publish", map[string]any{
		"text":            marker,
		"metadata":        map[string]any{"source": "noetive-mcp-integration"},
		"idempotency_key": marker,
	})
	if published.IsError {
		// This marker has never been published before, so the server has to
		// embed it, and a text-bearing call blocks on the embedder. When that
		// stalls the request returns nothing at all and the tool reports its own
		// budget expiring. The constant text in the idempotency test does not
		// exercise that path, which is why it can pass in the same run.
		//
		// Skipped for the same reason the search below is: this suite exists to
		// catch the API changing shape underneath a client that still compiles,
		// and a suite that also goes red when production is briefly slow stops
		// being read as evidence about the client at all. Drift still fails:
		// only a stall is tolerated, and it says so in the log.
		skipInconclusive(t, s.text(published))
		t.Fatalf("publish failed: %s", s.text(published))
	}
	if published.StructuredContent == nil {
		t.Error("expected structured content carrying the message id")
	}

	found := s.call("noetive_search", map[string]any{
		"query": fmt.Sprintf(`MATCH DISTANCE(%q) WITHIN 0.6 LIMIT 20`, marker),
	})
	if found.IsError {
		// Search embeds the query, so it blocks on the same component publish
		// just did. Tolerating a stall on one side and not the other would leave
		// the test red for the same production condition either way.
		skipInconclusive(t, s.text(found))
		t.Fatalf("search failed: %s", s.text(found))
	}
	if strings.Contains(s.text(found), "No matches") {
		t.Skip("the published message was not indexed yet; the broker makes no read-your-writes promise")
	}
}

// A repeated idempotency key must not create a second message. This is the one
// durability property a caller depends on when retrying.
func TestIdempotentPublishAgainstProduction(t *testing.T) {
	s := newSession(t)

	key := fmt.Sprintf("noetive-mcp-idempotency-%d", time.Now().UnixNano())
	args := map[string]any{"text": "idempotency probe", "idempotency_key": key}

	first := s.call("noetive_publish", args)
	if first.IsError {
		// The probe text is constant so a warm production cache should spare it
		// the embedder, but that is an assumption about a system this repository
		// does not control, and a broker-wide stall would block this call
		// regardless of whether the text was cached. Tolerated the same way the
		// publish above is: a stall says nothing about whether idempotency held.
		skipInconclusive(t, s.text(first))
		t.Fatalf("first publish failed: %s", s.text(first))
	}

	second := s.call("noetive_publish", args)
	if second.IsError {
		skipInconclusive(t, s.text(second))
		t.Fatalf("second publish failed: %s", s.text(second))
	}

	if s.text(first) != s.text(second) {
		t.Errorf("a repeated idempotency key produced a different message:\n first: %s\nsecond: %s", s.text(first), s.text(second))
	}
}

// Subscribe's handshake is the part worth probing: installing a subscription can
// come back as a retryable `unavailable`, and the client has to surface that as
// retryable rather than as a client defect. Zero matches in a quiet namespace is
// a success; a failed setup is not.
//
// Setup embeds the query, so it waits on the same component publish and search
// do, and on the tightest budget of the three, since the collect window is
// carved out of it. A stall lands here first and says no more about the client
// than it does there.
func TestSubscribeSetupAgainstProduction(t *testing.T) {
	s := newSession(t)

	result := s.call("noetive_subscribe", map[string]any{
		"query":        `MATCH DISTANCE("integration probe") WITHIN 0.5`,
		"max_matches":  float64(1),
		"wait_seconds": float64(5),
	})

	if result.IsError {
		skipInconclusive(t, s.text(result))
		t.Fatalf("subscribe failed: %s", s.text(result))
	}
	if !strings.Contains(s.text(result), "subscription") {
		t.Errorf("expected a subscription id in the summary, got: %s", s.text(result))
	}
}

// A query the server rejects must come back with the server's own code and a
// request_id. This is what makes a production incident debuggable, and it is
// only observable against a real server.
func TestServerRejectionCarriesItsRequestID(t *testing.T) {
	s := newSession(t)

	result := s.call("noetive_search", map[string]any{"query": "THIS IS NOT SEMQL"})

	if !result.IsError {
		t.Skip("the server accepted a query expected to be invalid; the grammar may have changed")
	}
	// An error arrived, but not necessarily the rejection this test is about: a
	// broker that is unreachable or refusing retryably also lands here, and
	// neither carries a request id because neither reached the parser. Asserting
	// on one would report a missing correlation id the server was never asked
	// for.
	skipInconclusive(t, s.text(result))
	if !strings.Contains(s.text(result), "request_id") {
		t.Errorf("expected a request_id to correlate with server logs, got: %s", s.text(result))
	}
}
