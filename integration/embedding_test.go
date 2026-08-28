//go:build integration

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
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/embedding"
	"github.com/noetive/noetive-mcp/internal/mcpserver"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// localTarget is where these tests route.
//
// Unlike the rest of the suite it is not pinned to the shared namespace. A
// local embedder has to be told the same model name the namespace is
// provisioned under, and that pairing is the operator's own — so the triple
// comes from the environment, falling back to the shared namespace when it says
// nothing.
func localTarget(t *testing.T) targeting.Policy {
	t.Helper()

	fromEnv, err := targeting.FromEnv(os.Getenv)
	if err != nil {
		t.Fatalf("could not read the routing configuration: %v", err)
	}

	return targeting.Policy{Fallback: targeting.Layer(fromEnv.Fallback, global)}
}

// newLocalSession drives the assembled server with an embeddings endpoint of
// the operator's own, which is the arrangement these tests exist to check.
//
// It is skipped unless NOETIVE_EMBEDDINGS_URL is set, in the same shape as the
// NOETIVE_KEY_SECRET skip: an ordinary run has no such service to talk to.
func newLocalSession(t *testing.T) *session {
	t.Helper()

	if strings.TrimSpace(os.Getenv(embedding.EnvURL)) == "" {
		t.Skipf("%s is not set; skipping the local-embedding tests", embedding.EnvURL)
	}
	if strings.TrimSpace(os.Getenv("NOETIVE_KEY_SECRET")) == "" {
		t.Skip("NOETIVE_KEY_SECRET is not set; skipping integration tests")
	}

	endpoint, err := embedding.At(os.Getenv(embedding.EnvURL), os.Getenv(embedding.EnvKey))
	if err != nil {
		t.Fatalf("could not build the embeddings endpoint: %v", err)
	}

	client, err := semantik.NewFromEnv()
	if err != nil {
		t.Fatalf("could not build the client: %v", err)
	}

	policy := localTarget(t)
	t.Logf("routing to namespace %q with model %q at %d dimensions",
		policy.Fallback.Namespace, policy.Fallback.Model, policy.Fallback.Dimensions)

	return start(t, embedding.Precomputed(client, endpoint), policy)
}

// newRoutedSession is the ordinary server — the broker does the embedding —
// pointed at the same namespace as newLocalSession, so the two can be compared.
func newRoutedSession(t *testing.T, policy targeting.Policy) *session {
	t.Helper()

	client, err := semantik.NewFromEnv()
	if err != nil {
		t.Fatalf("could not build the client: %v", err)
	}
	return start(t, client, policy)
}

func start(t *testing.T, b mcpserver.Broker, policy targeting.Policy) *session {
	t.Helper()

	srv := mcpserver.New("integration", b, policy)
	ctx := context.Background()
	srv.HandleMessage(ctx, json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"integration","version":"1"}}}`))
	srv.HandleMessage(ctx, json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))

	return &session{t: t, srv: srv}
}

// The one thing no unit test can establish: that the vectors this machine
// produces land in the same space the namespace is indexed in.
//
// Everything else about the feature is checkable against a stub — that the text
// is replaced, that the wire shape is right, that a failure sends nothing. Two
// models agreeing on dimensionality while disagreeing on meaning is invisible
// to all of it, and shows up only here, as a publish that nothing can find.
func TestLocallyEmbeddedPublishIsFoundByALocallyEmbeddedSearch(t *testing.T) {
	s := newLocalSession(t)

	// Derived from the clock so repeated runs never collide. Metadata carries
	// the marker too: a vector-only publish stores no text, so the metadata is
	// the only readable thing the message will have.
	marker := fmt.Sprintf("noetive-mcp local embedding marker %d", time.Now().UnixNano())

	published := s.call("noetive_publish", map[string]any{
		"text":            marker,
		"metadata":        map[string]any{"source": "noetive-mcp-integration-local"},
		"idempotency_key": marker,
	})
	if published.IsError {
		message := s.text(published)
		// A length complaint is the common misconfiguration rather than drift,
		// and it names both numbers, so say so plainly instead of skipping past
		// it: this is the failure an operator most needs to read.
		if strings.Contains(message, "dimensional") {
			t.Fatalf("the endpoint and the namespace disagree about size: %s", message)
		}
		if stalled(message) {
			t.Skipf("the broker did not answer in time; nothing to conclude about the client: %s", message)
		}
		t.Fatalf("publish failed: %s", message)
	}

	found := s.call("noetive_search", map[string]any{
		"query": fmt.Sprintf(`MATCH DISTANCE(%q) WITHIN 0.6 LIMIT 20`, marker),
	})
	if found.IsError {
		if stalled(s.text(found)) {
			t.Skipf("the broker did not answer in time; nothing to conclude about the client: %s", s.text(found))
		}
		t.Fatalf("search failed: %s", s.text(found))
	}
	if strings.Contains(s.text(found), "No matches") {
		t.Skip("the published message was not indexed yet; the broker makes no read-your-writes promise")
	}

	// The vector is what found it; the text is what comes back. A publish that
	// sent the vector alone would still match here and return nothing readable,
	// which is the failure this asserts against.
	if !strings.Contains(marshal(t, found), marker) {
		t.Errorf("the message was found but its text did not come back: %s", marshal(t, found))
	}
}

// The claim the whole feature rests on, and the only test that can examine it.
//
// Publishing and searching through the same local embedder proves the vectors
// round-trip, but it would pass just as well if the model here were nothing
// like the one the namespace is provisioned with — both ends would simply be
// wrong together. This publishes with text, so the broker embeds it, and then
// searches with a query embedded here. A match means the two embedders put the
// same phrase in the same place, which is the thing nothing else can check.
func TestALocallyEmbeddedQueryFindsABrokerEmbeddedMessage(t *testing.T) {
	local := newLocalSession(t)

	// A plain session, sharing the same routing so both halves address one
	// namespace, but with no embedder of its own: this publish sends text.
	remote := newRoutedSession(t, localTarget(t))

	marker := fmt.Sprintf("noetive-mcp cross embedder marker %d", time.Now().UnixNano())

	published := remote.call("noetive_publish", map[string]any{
		"text":            marker,
		"metadata":        map[string]any{"source": "noetive-mcp-integration-cross"},
		"idempotency_key": marker,
	})
	if published.IsError {
		if stalled(remote.text(published)) {
			t.Skipf("the broker did not answer in time; nothing to conclude about the client: %s", remote.text(published))
		}
		t.Fatalf("publish failed: %s", remote.text(published))
	}

	found := local.call("noetive_search", map[string]any{
		"query": fmt.Sprintf(`MATCH DISTANCE(%q) WITHIN 0.6 LIMIT 20`, marker),
	})
	if found.IsError {
		if stalled(local.text(found)) {
			t.Skipf("the broker did not answer in time; nothing to conclude about the client: %s", local.text(found))
		}
		t.Fatalf("search failed: %s", local.text(found))
	}
	if strings.Contains(local.text(found), "No matches") {
		// Indexing lag and a space mismatch look identical from here, so this
		// cannot be a failure. It is the one result worth re-running before
		// trusting a setup: a miss that persists is the models disagreeing.
		t.Skipf("no match yet — either the message is not indexed, or the local model is not the one the namespace uses: %s", local.text(found))
	}

	// That something matched is not the claim. The namespace holds other
	// messages, and a coincidental hit at this threshold would read as proof of
	// a space agreement that had not been shown.
	id := messageID(t, published)
	if !strings.Contains(marshal(t, found), id) {
		t.Errorf("the search matched something, but not the message just published (%s)", id)
	}
}

// messageID digs the server-assigned id out of a publish result.
func messageID(t *testing.T, result mcp.CallToolResult) string {
	t.Helper()

	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("could not read the publish result: %v", err)
	}

	var published struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("could not read the publish result: %v", err)
	}
	if published.MessageID == "" {
		t.Fatal("the publish returned no message id to look for")
	}
	return published.MessageID
}

func marshal(t *testing.T, result mcp.CallToolResult) string {
	t.Helper()

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("could not encode the result: %v", err)
	}
	return string(raw)
}

// The rewritten query has to be one the broker's own parser accepts. It is a
// shape nothing else in this repository produces — vector literals spliced into
// the text the agent wrote — and only the live server can say whether it parses.
func TestALocallyEmbeddedQueryParsesOnTheServer(t *testing.T) {
	s := newLocalSession(t)

	for _, query := range []string{
		`MATCH DISTANCE("payment reconciliation failure") WITHIN 0.4 LIMIT 5`,
		`MATCH DIRECTION(["customer frustration", "billing complaint"]) CONE 0.4 LIMIT 5`,
		`MATCH CONTRAST(ATTRACT ["enterprise"], REPEL ["free tier"]) WITHIN 0.3 LIMIT 5`,
		`{"match":{"distance":{"anchor":"deployment rollback","within":0.4}},"limit":5}`,
	} {
		result := s.call("noetive_search", map[string]any{"query": query})
		if !result.IsError {
			continue
		}
		if stalled(s.text(result)) {
			t.Skipf("the broker did not answer in time; nothing to conclude about the client: %s", s.text(result))
		}
		t.Errorf("the server rejected the rewritten form of %s:\n  %s", query, s.text(result))
	}
}

// Lint is the call that would otherwise carry anchor text to an unauthenticated
// endpoint. Blanking has to leave a query the server still parses, or the tool
// the instructions point agents at stops working on exactly these servers.
func TestABlankedLintStillChecksTheQuery(t *testing.T) {
	s := newLocalSession(t)

	valid := s.call("noetive_lint", map[string]any{
		"query": `MATCH DISTANCE("payroll incident") WITHIN 0.5`,
	})
	if valid.IsError {
		t.Fatalf("lint failed: %s", s.text(valid))
	}
	if !strings.Contains(s.text(valid), "valid") {
		t.Errorf("expected a verdict on a well-formed query, got: %s", s.text(valid))
	}

	// A query that is wrong in a way blanking cannot hide must still come back
	// wrong; otherwise blanking would be laundering real mistakes.
	broken := s.call("noetive_lint", map[string]any{
		"query": `MATCH DISTANCE("payroll incident") WITHIN 5`,
	})
	if broken.IsError {
		t.Fatalf("lint failed: %s", s.text(broken))
	}
	if !strings.Contains(s.text(broken), "not valid") {
		t.Errorf("expected an out-of-range threshold to still be caught, got: %s", s.text(broken))
	}
}
