package broker_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// The window has to close on the requested count, otherwise the tool holds the
// agent's turn open for the full wait even when it already has what it asked
// for.
func TestSubscribeStopsAtTheRequestedCount(t *testing.T) {
	stream := &fakeStream{id: "sub_01hz", events: []semantik.MatchEvent{
		{MessageID: "msg_1", Score: 0.9},
		{MessageID: "msg_2", Score: 0.8},
		{MessageID: "msg_3", Score: 0.7},
	}}
	stub := &stubBroker{stream: stream}
	_, handler := broker.SubscribeTool(stub, complete)

	result := call(t, handler, map[string]any{"query": "MATCH", "max_matches": float64(2), "wait_seconds": float64(60)})
	requireSuccess(t, result)

	if got := text(t, result); !strings.Contains(got, "2 matches") {
		t.Errorf("expected 2 matches, got: %s", got)
	}
	if stream.reads != 2 {
		t.Errorf("expected exactly 2 reads, got %d", stream.reads)
	}
}

// An interrupted stream must return what already arrived. Those matches really
// happened; discarding them loses information the agent cannot get back, since
// a new subscription gets a fresh id and the server makes no replay promise.
func TestInterruptedStreamKeepsWhatArrivedAndSaysSo(t *testing.T) {
	stream := &fakeStream{
		id:     "sub_01hz",
		events: []semantik.MatchEvent{{MessageID: "msg_1", Score: 0.9}},
		err:    &semantik.SubscribeStreamError{Cause: context.Canceled},
	}
	stub := &stubBroker{stream: stream}
	_, handler := broker.SubscribeTool(stub, complete)

	result := call(t, handler, map[string]any{"query": "MATCH", "wait_seconds": float64(60)})
	requireSuccess(t, result)

	got := text(t, result)
	if !strings.Contains(got, "1 matches") {
		t.Errorf("expected the collected match to survive, got: %s", got)
	}
	if !strings.Contains(got, "interrupted") {
		t.Errorf("expected the interruption to be reported, got: %s", got)
	}
}

// A setup failure and an in-flight failure need different remediation — retry
// versus reconnect-and-dedupe — so they must not read the same to the agent.
func TestSetupFailureIsDistinguishedFromAStreamFailure(t *testing.T) {
	stub := &stubBroker{subErr: &semantik.SubscribeSetupError{
		Err: &semantik.Error{Code: semantik.CodeUnavailable, Message: "setup budget exceeded", HTTPStatus: 503},
	}}
	_, handler := broker.SubscribeTool(stub, complete)

	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	if !strings.Contains(message, "could not start") {
		t.Errorf("expected the message to mark this as a setup failure, got: %s", message)
	}
}

// The subscription must be closed when the call returns. A leaked subscription
// keeps consuming server resources for a stream nobody is reading.
func TestSubscriptionIsClosedWhenTheCallReturns(t *testing.T) {
	stream := &fakeStream{id: "sub_01hz", events: []semantik.MatchEvent{{MessageID: "msg_1"}}}
	stub := &stubBroker{stream: stream}
	_, handler := broker.SubscribeTool(stub, complete)

	call(t, handler, map[string]any{"query": "MATCH", "max_matches": float64(1)})

	if !stream.closed {
		t.Error("expected the subscription to be closed")
	}
}

// The wait is capped so a tool call cannot hold the agent's turn open longer
// than the server promises. An over-large request is clamped rather than
// refused: it is a request to wait as long as allowed.
func TestOversizedWindowIsClampedNotRefused(t *testing.T) {
	stream := &fakeStream{id: "sub_01hz", events: []semantik.MatchEvent{{MessageID: "msg_1"}}}
	stub := &stubBroker{stream: stream}
	_, handler := broker.SubscribeTool(stub, complete)

	result := call(t, handler, map[string]any{
		"query":        "MATCH",
		"max_matches":  float64(1),
		"wait_seconds": float64(3600),
	})
	requireSuccess(t, result)

	if got := text(t, result); !strings.Contains(got, "1m0s") {
		t.Errorf("expected the window to be clamped to a minute, got: %s", got)
	}
}

// Same data-isolation boundary as publish and search: watching an unnamed
// namespace would stream someone else's traffic.
func TestSubscribeWithoutATargetNeverReachesTheBroker(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.SubscribeTool(stub, targeting.Target{})

	requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	if stub.subReq.Query != "" {
		t.Fatal("the broker was called despite an unresolved target")
	}
}

// fakeStream replays a fixed set of matches and then returns err, standing in
// for a live SSE subscription.
type fakeStream struct {
	err    error
	events []semantik.MatchEvent
	id     string

	mu     sync.Mutex
	reads  int
	closed bool
}

func (f *fakeStream) ID() string { return f.id }

func (f *fakeStream) Next(ctx context.Context) (semantik.MatchEvent, error) {
	if err := ctx.Err(); err != nil {
		return semantik.MatchEvent{}, err
	}

	f.mu.Lock()
	exhausted := f.reads >= len(f.events)
	var event semantik.MatchEvent
	if !exhausted {
		event = f.events[f.reads]
		f.reads++
	}
	f.mu.Unlock()

	if exhausted {
		if f.err != nil {
			return semantik.MatchEvent{}, f.err
		}
		// A quiet namespace: block until the window closes, holding no lock so
		// Close stays callable.
		<-ctx.Done()
		return semantik.MatchEvent{}, ctx.Err()
	}
	return event, nil
}

func (f *fakeStream) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}
