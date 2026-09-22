package broker_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
	"github.com/noetive/noetive-mcp/internal/embedding"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// complete is a fully-specified fallback, standing in for an operator who
// configured every routing field at startup.
var complete = targeting.Target{Namespace: "incidents", Model: "model-a", Dimensions: 512}

// configured is a server an operator fully set up and left the shared namespace
// open on, which is what most tests want to hold still while they exercise
// something else.
var configured = targeting.Policy{Fallback: complete}

// closedGlobal is the same server with the shared namespace closed.
var closedGlobal = targeting.Policy{Fallback: complete, GlobalDisabled: true}

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
		{"publish", handlerOf(broker.PublishTool(&stubBroker{publishErr: apiErr}, configured)), map[string]any{"text": "hello"}},
		{"search", handlerOf(broker.SearchTool(&stubBroker{searchErr: apiErr}, configured)), map[string]any{"query": "MATCH"}},
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

	_, handler := broker.SearchTool(&stubBroker{searchErr: apiErr}, configured)
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

	_, handler := broker.SearchTool(&stubBroker{searchErr: preflight}, configured)
	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	if !strings.Contains(message, "before the request was sent") {
		t.Errorf("expected the message to mark this as pre-flight, got: %s", message)
	}
}

// Transport failures are not wrapped in *semantik.Error by the SDK. They still
// have to reach the agent rather than being swallowed into a bare "failed".
//
// A plain error rather than a context one: those two are reported by their own
// branches below, so using one here would test that path twice and leave the
// generic one uncovered.
func TestTransportFailuresStillCarryTheirCause(t *testing.T) {
	cause := errors.New("dial tcp 10.0.0.1:443: connect: connection refused")
	_, handler := broker.HealthTool(&stubBroker{healthErr: cause})
	message := requireError(t, call(t, handler, map[string]any{}))

	if !strings.Contains(message, cause.Error()) {
		t.Errorf("expected the underlying cause in the message, got: %s", message)
	}
}

// "context deadline exceeded" names no deadline, no duration and no side, so a
// tool reporting it verbatim leaves the reader unable to tell our budget from
// the server's answer. It is the shape a stalled embedder takes, a text-bearing
// call blocks on it and nothing comes back, and naming the budget is what
// distinguishes that from a rejection, which arrives at once with a code.
func TestOurOwnBudgetExpiringSaysSoAndNamesIt(t *testing.T) {
	_, handler := broker.HealthTool(&stubBroker{healthErr: context.DeadlineExceeded})
	message := requireError(t, call(t, handler, map[string]any{}))

	if !strings.Contains(message, "no response within") {
		t.Errorf("expected the message to say nothing came back, got: %s", message)
	}
	// The duration, not just the fact. "no response within 10s" tells a reader
	// which call this was and how long it waited; without it they cannot tell a
	// probe from a query.
	if !strings.Contains(message, "10s") {
		t.Errorf("expected the message to name the budget that expired, got: %s", message)
	}
}

// A call can fail with two true facts at once: the server refused it with a
// code and a hint saying when to come back, and our budget then ran out while
// that wait was being honoured. The refusal is the one the agent can act on, so
// it is the one that has to be reported: saying the budget expired tells the
// agent nothing came back when something did.
func TestAServerRefusalOutranksOurOwnBudgetExpiring(t *testing.T) {
	refused := errors.Join(&semantik.Error{
		Code:       semantik.CodeUnavailable,
		Message:    "the embedder is not ready",
		RequestID:  "req_joined",
		RetryAfter: 19 * time.Second,
		HTTPStatus: 503,
	}, context.DeadlineExceeded)

	_, handler := broker.SearchTool(&stubBroker{searchErr: refused}, configured)
	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	for _, want := range []string{semantik.CodeUnavailable, "req_joined", "19s"} {
		if !strings.Contains(message, want) {
			t.Errorf("expected the refusal to reach the agent carrying %q, got: %s", want, message)
		}
	}
	if strings.Contains(message, "no response within") {
		t.Errorf("a refusal that did arrive was reported as nothing arriving: %s", message)
	}
}

// A subscription setup that merely ran out of time must still be reported as
// our budget. The SDK hands back an envelope it synthesised to stand in for the
// deadline, and printing that envelope's code would invent a server answer out
// of our own clock: sending whoever reads it to look for a request id that does
// not exist. What keeps it out of reach is that the envelope is carried beside
// the cause rather than in front of it, which is a property of the SDK's type
// and therefore worth a test on this side of the boundary.
func TestASubscriptionSetupThatTimedOutNamesOurBudget(t *testing.T) {
	timedOut := &semantik.SubscribeSetupError{
		Err:   &semantik.Error{Code: semantik.CodeMalformedResponse, Message: context.DeadlineExceeded.Error()},
		Cause: context.DeadlineExceeded,
	}

	_, handler := broker.SubscribeTool(&stubBroker{subErr: timedOut}, configured)
	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	if !strings.Contains(message, "no response within") {
		t.Errorf("expected our own budget to be named, got: %s", message)
	}
	if strings.Contains(message, semantik.CodeMalformedResponse) {
		t.Errorf("our own budget expiring was reported as a code the server sent: %s", message)
	}
}

// A connection that never opened is not the broker refusing and not our clock
// running out, and the raw dial error conveys that only to a reader who can
// parse one. Saying it plainly is what lets the integration suite tell an outage
// apart from wire drift, which is the only thing it measures.
func TestABrokerThatCannotBeReachedSaysSoRatherThanDumpingADialError(t *testing.T) {
	refused := &url.Error{
		Op:  "Post",
		URL: "https://semantik.noetive.io/v1/publish",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
	}

	_, handler := broker.PublishTool(&stubBroker{publishErr: refused}, configured)
	message := requireError(t, call(t, handler, map[string]any{"text": "hello"}))

	if !strings.Contains(message, broker.Unreachable) {
		t.Errorf("expected the message to say the host was never reached, got: %s", message)
	}
	// The cause survives, because "could not reach" alone does not distinguish a
	// refused port from a name that does not resolve. Compared against the errno's
	// own text rather than a fixed English phrase, which differs by platform.
	if !strings.Contains(message, syscall.ECONNREFUSED.Error()) {
		t.Errorf("expected the underlying cause to survive, got: %s", message)
	}
	// Nothing left this machine, so the agent can retry without wondering whether
	// the first attempt landed. Saying so is the whole reason this is separate
	// from a connection that broke after opening.
	if !strings.Contains(message, "nothing was sent") {
		t.Errorf("expected the message to settle whether the call had an effect, got: %s", message)
	}
	if strings.Contains(message, broker.NoResponseWithin) {
		t.Errorf("a connection that never opened was reported as a timeout: %s", message)
	}
	// The unwrapped cause is reported rather than the *url.Error, which carries
	// the method and full URL. Which path was attempted is not something the
	// agent can act on, and this is text an agent surfaces to a user.
	if strings.Contains(message, "/v1/publish") {
		t.Errorf("the request URL leaked into the message: %s", message)
	}
}

// http.Client wraps errors raised before a socket is ever touched in the same
// *url.Error it uses for a dial failure. Matching that wrapper rather than the
// cause would report this client being misconfigured as the broker being absent,
// and, because the integration suite skips on exactly that phrase, would have
// it excuse the class of defect it exists to catch.
//
// An API key carrying a trailing newline is the live version of this: the SDK
// checks the prefix and not the whitespace, so the value reaches the transport
// and is refused as a header. A secret pasted with a newline reproduces it.
func TestAMisconfiguredClientIsNotReportedAsTheBrokerBeingAbsent(t *testing.T) {
	// Rendered the way net/http renders them; each is a *url.Error with no net
	// type anywhere inside, which is what separates these from a real outage.
	scenarios := map[string]*url.Error{
		"api key with a trailing newline": {Op: "Post", URL: "https://semantik.noetive.io/v1/search",
			Err: errors.New(`net/http: invalid header field value for "Authorization"`)},
		"base url with a bad scheme": {Op: "Post", URL: "htp://semantik.noetive.io/v1/search",
			Err: errors.New(`unsupported protocol scheme "htp"`)},
		"base url that will not parse": {Op: "parse", URL: "http://exa mple.com/",
			Err: errors.New(`invalid character " " in host name`)},
	}

	for name, broken := range scenarios {
		t.Run(name, func(t *testing.T) {
			_, handler := broker.SearchTool(&stubBroker{searchErr: broken}, configured)
			message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

			if strings.Contains(message, broker.Unreachable) || strings.Contains(message, broker.Unanswered) {
				t.Errorf("a client-side defect was reported as the broker's: %s", message)
			}
			// It still has to reach the agent rather than being swallowed: this
			// is the one message that tells an operator their key or URL is wrong.
			if !strings.Contains(message, broken.Err.Error()) {
				t.Errorf("expected the cause to reach the agent, got: %s", message)
			}
		})
	}
}

// A connection that opened and then broke is not the same as one that never
// opened: the request went out, so the call may already have taken effect.
// Promising it did not would tell an agent a duplicate publish is safe.
func TestAConnectionThatBrokeAfterOpeningDoesNotPromiseTheCallHadNoEffect(t *testing.T) {
	scenarios := map[string]error{
		// The peer hung up without replying. No net type, so this is matched on
		// io.EOF rather than on *net.OpError.
		"peer hung up": &url.Error{Op: "Post", URL: "https://semantik.noetive.io/v1/publish", Err: io.EOF},
		// Reset while the request was being written.
		"reset in flight": &url.Error{Op: "Post", URL: "https://semantik.noetive.io/v1/publish",
			Err: &net.OpError{Op: "write", Net: "tcp", Err: syscall.ECONNRESET}},
	}

	for name, broken := range scenarios {
		t.Run(name, func(t *testing.T) {
			_, handler := broker.PublishTool(&stubBroker{publishErr: broken}, configured)
			message := requireError(t, call(t, handler, map[string]any{"text": "hello"}))

			if !strings.Contains(message, broker.Unanswered) {
				t.Errorf("expected the message to say no reply came back, got: %s", message)
			}
			if strings.Contains(message, "nothing was sent") {
				t.Errorf("a request that went out was reported as never sent: %s", message)
			}
			// The survivorship fact, not the mechanism: the agent has to know the
			// call may already have landed before it decides to repeat it.
			if !strings.Contains(message, "may already have taken effect") {
				t.Errorf("expected the message to carry the next action, got: %s", message)
			}
		})
	}
}

// failure renders for all five tools, but only publish accepts an idempotency
// key. Advice naming that argument would tell the other four to send something
// their schema does not offer, which is a wall an agent retries into rather than
// a next action.
func TestTheRetryAdviceNamesNothingATooldoesNotAccept(t *testing.T) {
	broken := &url.Error{Op: "Post", URL: "https://semantik.noetive.io/v1/search", Err: io.EOF}

	_, handler := broker.SearchTool(&stubBroker{searchErr: broken}, configured)
	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	if strings.Contains(message, "idempotency") {
		t.Errorf("a read tool was told to send an argument it does not accept: %s", message)
	}
	if !strings.Contains(message, broker.Unanswered) {
		t.Errorf("expected the message to say no reply came back, got: %s", message)
	}
}

// net/http wraps a dial failure a second time when a proxy is configured, and
// the outer wrapper's Op is "proxyconnect". Reading that one reports a call that
// never left the machine as one that may already have taken effect: the only
// error in this set whose wrong direction is unsafe rather than merely unhelpful.
func TestADialFailureBehindAProxyStillSaysNothingWasSent(t *testing.T) {
	behindProxy := &url.Error{
		Op:  "Post",
		URL: "https://semantik.noetive.io/v1/publish",
		Err: &net.OpError{Op: "proxyconnect", Net: "tcp",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}},
	}

	_, handler := broker.PublishTool(&stubBroker{publishErr: behindProxy}, configured)
	message := requireError(t, call(t, handler, map[string]any{"text": "hello"}))

	if !strings.Contains(message, broker.Unreachable) {
		t.Errorf("a dial failure behind a proxy was not reported as unreachable: %s", message)
	}
	if strings.Contains(message, "may already have taken effect") {
		t.Errorf("a call that never left the machine was reported as possibly landed: %s", message)
	}
	// The innermost error is the one that names the address and the syscall;
	// every layer above it names a wrapper.
	if strings.Contains(message, "proxyconnect") {
		t.Errorf("expected the innermost cause, got the wrapper: %s", message)
	}
}

// A TLS handshake that runs out of time carries no deadline identity and no net
// type, so it matches neither the budget branch nor the dial one. Left to the
// raw branch it reads as client drift and turns the suite red for a broker
// sitting behind a stalled terminator.
func TestATLSHandshakeTimeoutIsReportedAsUnreachable(t *testing.T) {
	// Stands in for net/http's unexported tlsHandshakeTimeoutError: a net.Error
	// whose Timeout reports true, wrapping nothing.
	stalled := &url.Error{Op: "Post", URL: "https://semantik.noetive.io/v1/health", Err: timeoutOnly{}}

	_, handler := broker.HealthTool(&stubBroker{healthErr: stalled})
	message := requireError(t, call(t, handler, map[string]any{}))

	if !strings.Contains(message, broker.Unreachable) {
		t.Errorf("expected a handshake timeout to read as unreachable, got: %s", message)
	}
}

// timeoutOnly mirrors the shape net/http uses for a handshake timeout: it
// satisfies net.Error and nothing else, with no Is for our own deadline.
type timeoutOnly struct{}

func (timeoutOnly) Error() string   { return "net/http: TLS handshake timeout" }
func (timeoutOnly) Timeout() bool   { return true }
func (timeoutOnly) Temporary() bool { return true }

// The subscribe handshake read arrives with no *url.Error around it, wrapped
// instead in the SDK's own chain. Rendering that chain would put doubled package
// prefixes and an internal function name into text an agent shows a user.
func TestTheSubscribeHandshakeReportsTheConditionNotTheSDKChain(t *testing.T) {
	handshake := fmt.Errorf("semantik: subscribe setup: %w",
		fmt.Errorf("semantik: subscribe: read subscribed frame: %w",
			&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}))

	_, handler := broker.SubscribeTool(&stubBroker{subErr: handshake}, configured)
	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	if !strings.Contains(message, broker.Unanswered) {
		t.Errorf("expected a broken handshake to read as no reply, got: %s", message)
	}
	if strings.Contains(message, "read subscribed frame") || strings.Contains(message, "semantik: subscribe") {
		t.Errorf("the SDK's internals reached the agent: %s", message)
	}
}

// A certificate this client will not trust is the broker being unreachable, not
// a malformed request. It arrives with no *net.OpError inside, so it needs its
// own arm rather than falling through to the raw branch.
func TestAnUntrustedCertificateIsReportedAsUnreachable(t *testing.T) {
	untrusted := &url.Error{
		Op:  "Post",
		URL: "https://semantik.noetive.io/v1/lint",
		Err: &tls.CertificateVerificationError{Err: errors.New("x509: certificate signed by unknown authority")},
	}

	_, handler := broker.LintTool(&stubBroker{lintErr: untrusted})
	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	if !strings.Contains(message, broker.Unreachable) {
		t.Errorf("expected an untrusted certificate to read as unreachable, got: %s", message)
	}
}

// Our own budget expiring is also wrapped in a *url.Error by http.Client, so the
// two branches are ordered rather than exclusive. Getting the order wrong would
// report every stalled call, the shape an embedder that is not ready takes, as
// a host that was never reachable, sending the reader to check DNS.
func TestOurOwnBudgetExpiringOutranksTheConnectionNeverOpening(t *testing.T) {
	stalled := &url.Error{
		Op:  "Post",
		URL: "https://semantik.noetive.io/v1/search",
		Err: context.DeadlineExceeded,
	}

	_, handler := broker.SearchTool(&stubBroker{searchErr: stalled}, configured)
	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	if !strings.Contains(message, "no response within") {
		t.Errorf("expected our own budget to be named, got: %s", message)
	}
	if strings.Contains(message, "could not reach the broker") {
		t.Errorf("a call that reached the broker and stalled was reported as unreachable: %s", message)
	}
}

// An embedder on this machine that cannot be reached must not be reported as
// Noetive being unreachable, or an operator whose own endpoint is down goes
// looking at a service that was fine. What keeps the two apart is that
// *embedding.Error implements no Unwrap, so errors.As cannot see a transport
// error inside it: a property of that type, and therefore worth a test on this
// side of the boundary.
func TestALocalEmbedderThatCannotBeReachedIsNotReportedAsTheBroker(t *testing.T) {
	// Built the way endpoint.Embed builds it: the transport failure is flattened
	// into Message, so its text is present while the error itself is not.
	local := &embedding.Error{
		Endpoint: "http://localhost:11434/v1/embeddings",
		Model:    "Qwen3-Embedding-4B",
		Message: (&url.Error{
			Op:  "Post",
			URL: "http://localhost:11434/v1/embeddings",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
		}).Error(),
	}

	_, handler := broker.PublishTool(&stubBroker{publishErr: local}, configured)
	message := requireError(t, call(t, handler, map[string]any{"text": "hello"}))

	if strings.Contains(message, broker.Unreachable) || strings.Contains(message, broker.Unanswered) {
		t.Errorf("a local endpoint being down was reported against Noetive: %s", message)
	}
	if !strings.Contains(message, "localhost:11434") {
		t.Errorf("expected the message to name the endpoint that failed, got: %s", message)
	}
	// The property the rendering rests on, asserted directly so a refactor that
	// gives *embedding.Error an Unwrap fails here rather than quietly starting
	// to report an endpoint on this machine as Noetive.
	var reachable *net.OpError
	if errors.As(error(local), &reachable) {
		t.Error("*embedding.Error now unwraps to a transport error, which would blame Noetive for a local endpoint")
	}
}

// An editor closing a session cancels every request in flight. Reporting that
// as a failure against Noetive sends people to check a service that was fine.
func TestACancelledCallIsNotReportedAsABrokerFailure(t *testing.T) {
	_, handler := broker.HealthTool(&stubBroker{healthErr: context.Canceled})
	message := requireError(t, call(t, handler, map[string]any{}))

	if !strings.Contains(message, "cancelled") {
		t.Errorf("expected the message to name the caller, got: %s", message)
	}
	if strings.Contains(message, "no response within") {
		t.Errorf("a cancellation was reported as a timeout: %s", message)
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
