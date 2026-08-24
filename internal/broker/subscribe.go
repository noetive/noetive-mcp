package broker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/targeting"
)

// Bounds on the collect window. An MCP tool call blocks the agent's turn, so
// the tool waits for a while and then reports what it saw — it never holds the
// turn open indefinitely the way a long-lived stream would.
const (
	defaultWait  = 15 * time.Second
	maxWait      = 60 * time.Second
	defaultMax   = 10
	maxMatchCap  = 100
	setupBudget  = 20 * time.Second
	streamNotice = "The stream was interrupted; the matches collected before that point are included. Matches that arrived after the interruption are lost — a new subscription gets a fresh id and the server makes no replay promise."
)

// Subscriber installs a standing SemQL subscription and returns its live match
// stream.
type Subscriber interface {
	Subscribe(ctx context.Context, req semantik.SubscribeRequest) (Stream, error)
}

// Stream is the part of a live subscription this tool consumes.
type Stream interface {
	ID() string
	Next(ctx context.Context) (semantik.MatchEvent, error)
	Close() error
}

// SubscriberFrom adapts an SDK client to Subscriber.
//
// The conversion exists because Go has no covariant return types: a method
// returning *semantik.Subscription does not satisfy an interface method
// returning Stream, even though the concrete type implements Stream. This is a
// language limitation, not an abstraction mismatch — the two contracts are the
// same contract. Doing the conversion once here keeps it out of every wiring
// site and out of the handler.
//
//	srv.AddTool(broker.SubscribeTool(broker.SubscriberFrom(client), configured))
func SubscriberFrom(c interface {
	Subscribe(ctx context.Context, req semantik.SubscribeRequest) (*semantik.Subscription, error)
}) Subscriber {
	return &subscriptionOpener{open: c.Subscribe}
}

type subscriptionOpener struct {
	open func(ctx context.Context, req semantik.SubscribeRequest) (*semantik.Subscription, error)
}

func (o *subscriptionOpener) Subscribe(ctx context.Context, req semantik.SubscribeRequest) (Stream, error) {
	sub, err := o.open(ctx, req)
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// collected is the structured result of a bounded collect.
//
// InterruptReason and RequestID are populated only alongside Interrupted, and
// exist because a bare bool told an operator a stream broke without saying why
// or giving them anything to quote. The SDK carries a structured *semantik.Error
// on a mid-stream failure — code, message, request id — and all of it used to be
// discarded here, while the same detail was surfaced faithfully for a setup
// failure a few lines away.
//
// Field ordering: strings (16 B each) > slice (24 B) > bools.
type collected struct {
	SubscriptionID  string        `json:"subscription_id"`
	InterruptReason string        `json:"interrupt_reason,omitempty"`
	RequestID       string        `json:"request_id,omitempty"`
	Matches         []streamMatch `json:"matches"`
	ReachedLimit    bool          `json:"reached_limit"`
	Interrupted     bool          `json:"interrupted"`
}

// streamMatch is one live match. It carries an identifier and a score and no
// content, because that is all the server sends on a match frame.
//
// Field ordering: string (16 B) > float32 (4 B).
type streamMatch struct {
	MessageID string  `json:"message_id"`
	Score     float32 `json:"score"`
}

// SubscribeTool builds the noetive_subscribe tool and its handler.
//
// The tool opens a subscription, gathers matches until the requested count or
// the requested wait elapses, then closes it. Nothing survives the call: this
// is a bounded look at live traffic, not a standing subscription.
//
//	tool, handler := broker.SubscribeTool(client, configured)
//	srv.AddTool(tool, handler)
func SubscribeTool(s Subscriber, policy targeting.Policy) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	options := []mcp.ToolOption{
		mcp.WithDescription(
			"Watch a Noetive Semantik namespace for live messages matching a SemQL query, for up to a minute, then report what arrived. " +
				"Matches come back as message ids and scores only — the server does not send message content on a live match, so use noetive_search to read what a message says. " +
				"The subscription is closed when the call returns; it does not keep running.",
		),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("SemQL query describing the region of meaning to watch, for example: MATCH DISTANCE(\"gpu shortage\") WITHIN 0.5"),
		),
		mcp.WithNumber("max_matches",
			mcp.Description(fmt.Sprintf("Stop early once this many matches have arrived. Defaults to %d, capped at %d.", defaultMax, maxMatchCap)),
			mcp.Min(1),
			mcp.Max(maxMatchCap),
		),
		mcp.WithNumber("wait_seconds",
			mcp.Description(fmt.Sprintf("How long to watch before reporting. Defaults to %d, capped at %d.", int(defaultWait.Seconds()), int(maxWait.Seconds()))),
			mcp.Min(1),
			mcp.Max(maxWait.Seconds()),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	}
	options = append(options, targetingOptions(policy)...)

	tool := mcp.NewTool("noetive_subscribe", options...)

	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := request.RequireString("query")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("noetive_subscribe: invalid arguments", err), nil
		}

		requested, err := requestedTarget(request)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("noetive_subscribe: invalid arguments", err), nil
		}
		target, err := policy.Resolve(requested)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("noetive_subscribe", err), nil
		}

		limit := bound(request.GetInt("max_matches", defaultMax), 1, maxMatchCap)
		wait := time.Duration(bound(request.GetInt("wait_seconds", int(defaultWait.Seconds())), 1, int(maxWait.Seconds()))) * time.Second

		// The stream's lifetime is the context passed to Subscribe, and nothing
		// else can end a blocked read — Subscription.Next takes a context it
		// documents as sampled once and then ignored, because bufio.Scanner.Scan
		// blocks underneath it. So the window has to be closed from out here.
		//
		// Cancel rather than a timeout, armed after Subscribe returns: setup is
		// then measured rather than added. The previous deadline of
		// setupBudget+wait meant a 15s watch always held the agent's turn for 35s,
		// whether setup took twenty seconds or none.
		ctx, cancel := context.WithTimeout(ctx, setupBudget+wait)
		defer cancel()

		sub, err := s.Subscribe(ctx, semantik.SubscribeRequest{
			Query:      query,
			Namespace:  target.Namespace,
			Model:      target.Model,
			Dimensions: target.Dimensions,
		})
		if err != nil {
			var setup *semantik.SubscribeSetupError
			if errors.As(err, &setup) {
				return failure("noetive_subscribe could not start the subscription", setupBudget+wait, err), nil
			}
			return failure("noetive_subscribe", setupBudget+wait, err), nil
		}
		// Ignored deliberately: the collect window is over either way, and a
		// close error tells the agent nothing it can act on.
		defer func() { _ = sub.Close() }()

		// The window starts now, and closing it is what makes the read return.
		// collect is told when it ends so it can tell that cancellation apart from
		// a stream that genuinely broke — the SDK wraps both identically.
		closesAt := time.Now().Add(wait)
		windowOver := time.AfterFunc(wait, cancel)
		defer windowOver.Stop()

		result := collect(ctx, sub, limit, closesAt)
		return mcp.NewToolResultStructured(result, describe(target.Namespace, wait, result)), nil
	}

	return tool, handler
}

// collect reads matches until the limit is reached, the window closes, or the
// stream breaks. A broken stream is reported alongside whatever arrived first
// rather than discarding it: those matches really did happen, and an agent that
// gets nothing back cannot tell a quiet namespace from a dropped connection.
//
// closesAt is when the caller will cancel the stream context. It is needed
// because the SDK reports the resulting cancellation as a *SubscribeStreamError
// — the same type a genuinely dropped connection produces — so the type alone
// cannot distinguish "we stopped listening" from "the connection failed".
// Classifying on it is what stops an ordinary quiet window being reported as an
// interruption, which is what every idle call used to say.
func collect(ctx context.Context, sub Stream, limit int, closesAt time.Time) collected {
	result := collected{
		Matches:        make([]streamMatch, 0, limit),
		SubscriptionID: sub.ID(),
	}

	for len(result.Matches) < limit {
		// The per-read context is deliberately the stream context itself. Passing
		// a shorter one would read as a per-read deadline while doing nothing:
		// Next ignores the context it is handed once the read has begun.
		event, err := sub.Next(ctx)
		if err != nil {
			// A cancellation at or after the window's end is the window closing,
			// not a failure — including the wrapped form the SDK produces for it.
			// Anything else really did break the stream.
			if !isWindowClose(err, closesAt) {
				var stream *semantik.SubscribeStreamError
				if errors.As(err, &stream) {
					result.Interrupted = true
					result.InterruptReason, result.RequestID = describeStreamError(stream)
				}
			}
			return result
		}

		result.Matches = append(result.Matches, streamMatch{MessageID: event.MessageID, Score: event.Score})
	}

	result.ReachedLimit = true
	return result
}

// isWindowClose reports whether err is this tool ending its own watch rather
// than the stream failing.
//
// Both arrive as a *SubscribeStreamError wrapping a context error, so the type
// cannot separate them and the cause is checked against the clock: a
// cancellation once the window is over is the window, and one before it is not.
// The margin absorbs the gap between the timer firing and the read returning.
func isWindowClose(err error, closesAt time.Time) bool {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return !time.Now().Before(closesAt.Add(-windowCloseSlack))
}

// windowCloseSlack is how early a cancellation may land and still count as the
// window closing rather than a failure.
const windowCloseSlack = 250 * time.Millisecond

// describeStreamError extracts what an operator needs from a mid-stream failure:
// why it broke, and the request id to quote when asking.
//
// Err is best-effort on the SDK's side — it is nil when no structured shape
// could be synthesised — so the raw cause is the fallback rather than an empty
// string. Reporting "the stream was interrupted" with nothing after it is what
// made this undiagnosable from the client.
func describeStreamError(stream *semantik.SubscribeStreamError) (reason, requestID string) {
	if stream.Err != nil {
		reason = stream.Err.Code
		if stream.Err.Message != "" {
			reason += ": " + stream.Err.Message
		}
		return reason, stream.Err.RequestID
	}
	if stream.Cause != nil {
		return stream.Cause.Error(), ""
	}
	return "", ""
}

// describe renders the text fallback, stating what bounded the call so an agent
// can tell "nothing is happening" from "I stopped looking".
func describe(namespace string, wait time.Duration, result collected) string {
	var b strings.Builder
	b.Grow(len(namespace) + len(result.SubscriptionID) + len(streamNotice) + 64)

	b.WriteString(strconv.Itoa(len(result.Matches)))
	b.WriteString(" matches in ")
	b.WriteString(namespace)
	b.WriteString(" over ")
	b.WriteString(wait.String())
	b.WriteString(" (subscription ")
	b.WriteString(result.SubscriptionID)
	b.WriteString(").")

	switch {
	case result.Interrupted:
		b.WriteByte(' ')
		b.WriteString(streamNotice)
		// The reason and the request id are what turn "something broke" into
		// something an operator can look up. Both are omitted when the SDK could
		// not supply them rather than printed empty.
		if result.InterruptReason != "" {
			b.WriteString(" Cause: ")
			b.WriteString(result.InterruptReason)
			b.WriteByte('.')
		}
		if result.RequestID != "" {
			b.WriteString(" (request_id=")
			b.WriteString(result.RequestID)
			b.WriteByte(')')
		}
	case result.ReachedLimit:
		b.WriteString(" Stopped at the requested limit; more may have been available.")
	default:
		b.WriteString(" Watched for the full window.")
	}

	return b.String()
}

// bound clamps v into [low, high]. Arguments are clamped rather than rejected
// because the schema already advertises the range, and a slightly-too-large
// wait is a request to wait as long as allowed, not a mistake worth failing on.
func bound(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}
