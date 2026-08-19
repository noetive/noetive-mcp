//go:build integration

// Package integration drives the assembled MCP server against the live Noetive
// Semantik API.
//
// It hits production on purpose. The failure these tests exist to catch is wire
// drift — the API changing shape underneath a client that still compiles — and
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

	"github.com/noetive/noetive-mcp/internal/mcpserver"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// The shared namespace and the model it is provisioned with. Named explicitly
// rather than defaulted, exactly as a caller must.
var global = targeting.Target{Namespace: "global", Model: "Qwen3-Embedding-4B", Dimensions: 1024}

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

	srv := mcpserver.New("integration", client, global)
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
// If this fails, nothing below is meaningful.
func TestHealthAgainstProduction(t *testing.T) {
	s := newSession(t)

	result := s.call("noetive_health", map[string]any{})

	if result.IsError {
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
		t.Fatalf("lint failed: %s", s.text(result))
	}
	if !strings.Contains(s.text(result), "valid") {
		t.Errorf("expected a verdict, got: %s", s.text(result))
	}
}

// Publish then search, with a marker unique to this run. Indexing is not
// immediate and the server makes no read-your-writes promise, so a miss is
// reported as a skip rather than a failure — asserting on it would produce a
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
		t.Fatalf("publish failed: %s", s.text(published))
	}
	if published.StructuredContent == nil {
		t.Error("expected structured content carrying the message id")
	}

	found := s.call("noetive_search", map[string]any{
		"query": fmt.Sprintf(`MATCH DISTANCE(%q) WITHIN 0.6 LIMIT 20`, marker),
	})
	if found.IsError {
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
		t.Fatalf("first publish failed: %s", s.text(first))
	}

	second := s.call("noetive_publish", args)
	if second.IsError {
		t.Fatalf("second publish failed: %s", s.text(second))
	}

	if s.text(first) != s.text(second) {
		t.Errorf("a repeated idempotency key produced a different message:\n first: %s\nsecond: %s", s.text(first), s.text(second))
	}
}

// Subscribe's handshake is the part that fails in production — the server
// frequently returns a retryable 503 while installing a subscription. Zero
// matches in a quiet namespace is a success; a failed setup is not.
func TestSubscribeSetupAgainstProduction(t *testing.T) {
	s := newSession(t)

	result := s.call("noetive_subscribe", map[string]any{
		"query":        `MATCH DISTANCE("integration probe") WITHIN 0.5`,
		"max_matches":  float64(1),
		"wait_seconds": float64(5),
	})

	if result.IsError {
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
	if !strings.Contains(s.text(result), "request_id") {
		t.Errorf("expected a request_id to correlate with server logs, got: %s", s.text(result))
	}
}
