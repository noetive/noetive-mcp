// Package broker exposes Noetive Semantik operations as MCP tools.
//
// Each operation lives in its own file and owns the whole path for that one
// call: the tool descriptor an agent reads, the decoding of the arguments it
// sends, the SDK call, and the shaping of the result. Keeping the schema beside
// the call it describes is what stops the two drifting — a new argument cannot
// be added to one without being visible in the other.
//
// Every operation declares the narrow interface it needs from the SDK client,
// so tests inject a fake for that one method rather than a five-method double.
package broker

import (
	"context"
	"errors"
	"fmt"
	"math"
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
// whichever embedder is in play — Noetive's, or one on this machine when the
// operator configured an endpoint, in which case both the embed and the broker
// call share this one budget.
//
// Subscribe is excluded — its bound is the caller's collect window, see
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

// failure renders an error as a tool result the agent can act on.
//
// Every field the server sent is preserved. The code says what class of
// failure it is, the retry hint says whether and when to try again, and the
// request id is what a human quotes to support. Collapsing these into a single
// opaque string is what makes a transient 503 indistinguishable from a
// permanent misconfiguration.
func failure(operation string, budget time.Duration, err error) *mcp.CallToolResult {
	// Our own budget expiring is not the server saying anything, and reads
	// identically to one that did: "context deadline exceeded" names no
	// deadline, no duration and no side. Saying which budget ran out is what
	// separates an upstream that never answered — the shape a stalled embedder
	// takes, since a text-bearing call blocks on it — from a rejection, which
	// arrives in milliseconds carrying a code and a request id.
	//
	// A stall in an embedder on this machine does not reach here: it arrives as
	// an *embedding.Error naming the endpoint, which falls through to the raw
	// branch below rather than being reported against Noetive.
	if errors.Is(err, context.DeadlineExceeded) {
		return mcp.NewToolResultErrorf("%s failed: no response within %s (nothing was returned, so there is no code or request id to quote)", operation, budget)
	}
	// The caller withdrawing is not a failure of the call. An editor closing a
	// session cancels every request in flight, and reporting that as an error
	// against Noetive sends people to check a service that was fine.
	if errors.Is(err, context.Canceled) {
		return mcp.NewToolResultErrorf("%s failed: the caller cancelled the request before it completed", operation)
	}

	var apiErr *semantik.Error
	if !errors.As(err, &apiErr) {
		// Transport and preflight-free errors arrive raw from the SDK.
		return mcp.NewToolResultErrorf("%s failed: %s", operation, err)
	}

	// Built with direct writes rather than Fprintf. This runs on every failed
	// tool call, and a transient 503 from the broker is routine rather than
	// exceptional, so the error path is as hot as the success path. Sizing the
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
