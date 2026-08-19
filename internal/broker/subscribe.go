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
// Field ordering: string (one pointer at offset 0) > slice > bools.
type collected struct {
	SubscriptionID string        `json:"subscription_id"`
	Matches        []streamMatch `json:"matches"`
	ReachedLimit   bool          `json:"reached_limit"`
	Interrupted    bool          `json:"interrupted"`
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
func SubscribeTool(s Subscriber, fallback targeting.Target) (mcp.Tool, mcpserver.ToolHandlerFunc) {
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
	options = append(options, targetingOptions()...)

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
		target, err := targeting.Resolve(requested, fallback)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("noetive_subscribe", err), nil
		}

		limit := bound(request.GetInt("max_matches", defaultMax), 1, maxMatchCap)
		wait := time.Duration(bound(request.GetInt("wait_seconds", int(defaultWait.Seconds())), 1, int(maxWait.Seconds()))) * time.Second

		// One deadline covers the handshake and the whole collect window: the
		// SDK ties the stream's lifetime to the context passed to Subscribe,
		// so a shorter setup context would tear the stream down on first read.
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
				return failure("noetive_subscribe could not start the subscription", err), nil
			}
			return failure("noetive_subscribe", err), nil
		}
		// Ignored deliberately: the collect window is over either way, and a
		// close error tells the agent nothing it can act on.
		defer func() { _ = sub.Close() }()

		result := collect(ctx, sub, limit, wait)
		return mcp.NewToolResultStructured(result, describe(target.Namespace, wait, result)), nil
	}

	return tool, handler
}

// collect reads matches until the limit is reached, the window closes, or the
// stream breaks. A broken stream is reported alongside whatever arrived first
// rather than discarding it: those matches really did happen, and an agent that
// gets nothing back cannot tell a quiet namespace from a dropped connection.
func collect(ctx context.Context, sub Stream, limit int, wait time.Duration) collected {
	result := collected{
		Matches:        make([]streamMatch, 0, limit),
		SubscriptionID: sub.ID(),
	}

	deadline := time.Now().Add(wait)
	for len(result.Matches) < limit {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return result
		}

		read, cancel := context.WithTimeout(ctx, remaining)
		event, err := sub.Next(read)
		cancel()

		if err != nil {
			var stream *semantik.SubscribeStreamError
			if errors.As(err, &stream) {
				result.Interrupted = true
			}
			// A deadline or cancellation is the window closing, not a failure.
			return result
		}

		result.Matches = append(result.Matches, streamMatch{MessageID: event.MessageID, Score: event.Score})
	}

	result.ReachedLimit = true
	return result
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
