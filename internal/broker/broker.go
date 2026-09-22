// Package broker exposes Noetive Semantik operations as MCP tools.
//
// Each operation lives in its own file and owns the whole path for that one
// call: the tool descriptor an agent reads, the decoding of the arguments it
// sends, the SDK call, and the shaping of the result. Keeping the schema beside
// the call it describes is what stops the two drifting: a new argument cannot
// be added to one without being visible in the other.
//
// Every operation declares the narrow interface it needs from the SDK client,
// so tests inject a fake for that one method rather than a five-method double.
package broker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/targeting"
)

// Per-call deadlines. An MCP tool call blocks the agent's turn, so an
// unbounded call reads to the user as a hung editor. The values differ by
// what each call costs: health and lint are answered without reaching into a
// namespace, while publish and search reach across a whole one and wait on
// whichever embedder is in play: Noetive's, or one on this machine when the
// operator configured an endpoint, in which case both the embed and the broker
// call share this one budget.
//
// Subscribe is excluded: its bound is the caller's collect window, see
// subscribe.go.
const (
	probeTimeout = 10 * time.Second
	queryTimeout = 30 * time.Second
)

// targetingOptions describes the routing triple identically on every tool.
// Declared once because three tools carry the same three arguments and a
// divergent description would teach the agent contradictory rules.
//
// The namespace example tracks the policy. "global" is the obvious thing to
// name in a description, and an agent reads a description as a suggestion, so
// leaving it in place on a server that closes the shared namespace would send
// every unconfigured call straight into a refusal.
func targetingOptions(policy targeting.Policy) []mcp.ToolOption {
	namespace := "Namespace to route this call to, for example \"global\". Required unless the server was started with one configured. There is no default: an unnamed namespace is an error, never a shared space."
	if policy.GlobalDisabled {
		namespace = "Namespace to route this call to. Required unless the server was started with one configured. There is no default: an unnamed namespace is an error, never a shared space. The shared \"global\" namespace is closed on this server."
	}

	return []mcp.ToolOption{
		mcp.WithString("namespace",
			mcp.Description(namespace),
		),
		mcp.WithString("model",
			mcp.Description("Embedding model provisioned on the namespace, for example \"Qwen3-Embedding-4B\". Required unless the server was started with one configured."),
		),
		mcp.WithNumber("dimensions",
			mcp.Description("Embedding dimensionality of the model, for example 1024. Must match the model; a mismatch is rejected by the server. Required unless the server was started with one configured."),
			mcp.Min(1),
			mcp.Max(math.MaxUint16),
		),
	}
}

// requestedTarget reads the routing triple an agent sent. Absent arguments stay
// zero so targeting.Resolve can layer the configured fallback underneath them.
//
// A dimensions value outside uint16 is reported rather than truncated: silently
// wrapping 65537 to 1 would send a plausible-looking but wrong dimensionality.
func requestedTarget(request mcp.CallToolRequest) (targeting.Target, error) {
	t := targeting.Target{
		Namespace: strings.TrimSpace(request.GetString("namespace", "")),
		Model:     strings.TrimSpace(request.GetString("model", "")),
	}

	dims := request.GetInt("dimensions", 0)
	if dims < 0 || dims > math.MaxUint16 {
		return targeting.Target{}, fmt.Errorf("dimensions must be between 1 and %d, got %d", math.MaxUint16, dims)
	}
	t.Dimensions = uint16(dims)

	return t, nil
}

// The three conditions this package names in its own words rather than the
// server's, because in each of them the server said nothing at all.
//
// Exported so the integration suite can classify a failure as the broker's
// rather than this client's by matching what this package actually produces.
// A copy of the literal on that side is one that can drift silently, and the
// two ways it drifts are both bad: the suite goes red for an outage nobody
// here caused, or green for a defect it exists to catch.
//
// The server's own vocabulary is deliberately not mirrored here. Codes and
// retry hints belong to whoever sent them, and pinning them on this side would
// make every addition to that vocabulary a change in two repositories.
const (
	Unreachable      = "could not reach the broker"
	Unanswered       = "no reply from the broker"
	NoResponseWithin = "no response within"
)

// innermostOpError walks to the deepest *net.OpError in the chain, or nil.
//
// errors.As stops at the outermost, and net/http wraps a dial failure a second
// time when a proxy is configured: &net.OpError{Op: "proxyconnect", Err: <the
// dial OpError>}. Reading Op off the outer one sees "proxyconnect", not "dial",
// and reports a connection that never opened as one that broke: the single
// mistake in this file that must not happen, because it tells an agent a publish
// may have landed when nothing left the machine. Any host with HTTP_PROXY set
// takes that path on every outage.
//
// The innermost is also the one worth showing. It names the address and the
// syscall; every layer above it names a wrapper.
func innermostOpError(err error) *net.OpError {
	var innermost *net.OpError
	for {
		var op *net.OpError
		if !errors.As(err, &op) {
			return innermost
		}
		innermost, err = op, op.Err
	}
}

// failure renders an error as a tool result the agent can act on.
//
// Every field the server sent is preserved. The code says what class of
// failure it is, the retry hint says whether and when to try again, and the
// request id is what a human quotes to support. Collapsing these into a single
// opaque string is what makes a transient 503 indistinguishable from a
// permanent misconfiguration.
func failure(operation string, budget time.Duration, err error) *mcp.CallToolResult {
	// The caller withdrawing is not a failure of the call, and it outranks
	// everything below including a refusal the server had already sent: an
	// editor closing a session cancels every request in flight, and reporting
	// that as an error against Noetive sends people to check a service that was
	// fine.
	if errors.Is(err, context.Canceled) {
		return mcp.NewToolResultErrorf("%s failed: the caller cancelled the request before it completed", operation)
	}

	// A structured answer and our own budget expiring can both be true at once:
	// the call came back refused with a hint saying when to return, and the
	// budget ran out while that wait was being honoured. The answer is the more
	// useful of the two (it names what happened and what to do next), and
	// reporting the deadline instead tells the agent nothing came back when
	// something did.
	//
	// Anything reachable here was structured by whoever could describe the
	// failure: the server, or this client refusing before it sent. The envelope
	// the SDK synthesises to stand in for a deadline is not reachable (it is
	// carried beside the cause rather than in front of it), so this cannot
	// report our own clock under a code no server sent.
	var apiErr *semantik.Error
	if !errors.As(err, &apiErr) {
		// Our own budget expiring is not the server saying anything, and reads
		// identically to one that did: "context deadline exceeded" names no
		// deadline, no duration and no side. Saying which budget ran out is
		// what separates an upstream that never answered (the shape an
		// embedder that is not ready takes, since a text-bearing call blocks on
		// it) from a rejection, which arrives carrying a code and a request
		// id. The parenthetical is true by construction: anything structured
		// never reaches here.
		//
		// A stall in an embedder on this machine does not reach here: it
		// arrives as an *embedding.Error naming the endpoint, which falls
		// through to the raw branch below rather than being reported against
		// Noetive.
		if errors.Is(err, context.DeadlineExceeded) {
			return mcp.NewToolResultErrorf("%s failed: "+NoResponseWithin+" %s (nothing was returned, so there is no code or request id to quote)", operation, budget)
		}

		// What an agent can act on is the condition, not the wrappers above it:
		// the method and URL http.Client prefixes on, and (on the subscribe
		// handshake, which arrives with no *url.Error at all) the SDK's own
		// chain, which would otherwise put doubled package prefixes and an
		// internal function name into text an agent shows a user.
		op := innermostOpError(err)
		cause := err
		switch {
		case op != nil:
			cause = op
		default:
			var wrapped *url.Error
			if errors.As(err, &wrapped) {
				cause = wrapped.Err
			}
		}

		// The broker never answered, which is a different thing from the broker
		// refusing and needs saying in its own words: the raw form below says
		// it only to a reader who can parse a dial error.
		//
		// Matched on the cause's type rather than on *url.Error, which would be
		// far too wide: http.Client also wraps errors raised before a socket is
		// touched, and "invalid header field value" (an API key carrying a
		// newline) or "unsupported protocol scheme" are this client being
		// misconfigured, not the broker being absent. Reporting those as the
		// broker's fault would have the integration suite excuse a defect it
		// exists to catch, so they fall to the raw branch instead.
		//
		// Our own budget expiring is caught above and stays there. So is the
		// transport's own header timeout, which reports itself as a deadline:
		// both mean a request was sent and nothing came back in time, which the
		// branch above already says. A TLS handshake timing out does not: it
		// carries no deadline identity and no net type, so it is caught here.
		//
		// A local embedder cannot reach here: it arrives as an *embedding.Error,
		// which implements no Unwrap for exactly this reason, so errors.As
		// cannot see the transport error inside it and blame Noetive for an
		// endpoint on this machine.
		//
		// Both of these are enumerations, so both have misses; what differs is
		// which way a miss falls. A transport shape carrying no net or io type
		// ("server gave HTTP response to HTTPS client" when termination is
		// dropped on a balancer, or an HTTP/2 GOAWAY while one drains) lands on
		// the raw branch and turns the suite red. That is the direction to miss
		// in. A red run for an outage is read and closed by a person; excusing a
		// defect is silent, and this whole classification exists because the
		// suite must not go green on one.
		var (
			dnsErr  *net.DNSError
			certErr *tls.CertificateVerificationError
			netErr  net.Error
		)

		// Nothing left this machine: the name did not resolve, the connection
		// was refused, the peer could not be trusted, or the handshake never
		// finished. Worth separating from the case below because only here can
		// the caller retry without wondering whether the first attempt landed.
		if errors.As(err, &dnsErr) || errors.As(err, &certErr) ||
			(op != nil && op.Op == "dial") ||
			(op == nil && errors.As(err, &netErr) && netErr.Timeout()) {
			return mcp.NewToolResultErrorf("%s failed: "+Unreachable+" (%s); nothing was sent, so nothing was changed", operation, cause)
		}

		// The connection opened and then broke: a reset, or a peer that hung up
		// without replying. The request went out, so this deliberately does not
		// promise the call had no effect.
		//
		// The advice stops at what is true for all five tools. Only publish can
		// be made safe to repeat, and only publish's own schema offers the
		// argument that does it, so naming that argument here would tell four
		// tools to send something they do not accept.
		if op != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return mcp.NewToolResultErrorf("%s failed: "+Unanswered+" (%s); the request was sent and may already have taken effect, so retry only if repeating it is safe", operation, cause)
		}

		// Transport and preflight-free errors arrive raw from the SDK.
		return mcp.NewToolResultErrorf("%s failed: %s", operation, err)
	}

	// Built with direct writes rather than Fprintf. This runs on every failed
	// tool call, and a retryable failure is one an agent is expected to act on
	// and retry, so the error path is as hot as the success path. Sizing the
	// buffer up front is what keeps it to a single allocation instead of one
	// per doubling.
	var b strings.Builder
	b.Grow(len(operation) + len(apiErr.Code) + len(apiErr.Message) + len(apiErr.RequestID) + 64)

	b.WriteString(operation)
	b.WriteString(" failed [")
	b.WriteString(apiErr.Code)
	b.WriteByte(']')

	if apiErr.Message != "" {
		b.WriteString(": ")
		b.WriteString(apiErr.Message)
	}
	if apiErr.HTTPStatus == 0 {
		b.WriteString(" (rejected before the request was sent)")
	}
	if apiErr.RetryAfter > 0 {
		b.WriteString(" (retry after ")
		b.WriteString(apiErr.RetryAfter.String())
		b.WriteByte(')')
	}
	if apiErr.RequestID != "" {
		b.WriteString(" (request_id=")
		b.WriteString(apiErr.RequestID)
		b.WriteByte(')')
	}

	return mcp.NewToolResultError(b.String())
}
