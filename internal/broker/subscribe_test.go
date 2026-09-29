package broker_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"go.noetive.io/noetive-sdk-go/semantik"

	"go.noetive.io/noetive-mcp/internal/broker"
	"go.noetive.io/noetive-mcp/internal/targeting"
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
	_, handler := broker.SubscribeTool(stub, configured)

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
	_, handler := broker.SubscribeTool(stub, configured)

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

// A clean server-side close is what Subscription.Next returns io.EOF for, a
// broker draining during a deploy, and it arrives raw rather than wrapped in a
// *SubscribeStreamError. Classifying on that type alone let it fall through to
// the healthy path, so a watch that had already ended was reported as still
// running over a quiet namespace. The two are the opposite advice: one says
// nothing is being published, the other says reconnect.
func TestCleanServerCloseIsReportedRatherThanReadAsQuiet(t *testing.T) {
	stream := &fakeStream{
		id:     "sub_01hz",
		events: []semantik.MatchEvent{{MessageID: "msg_1", Score: 0.9}},
		err:    io.EOF,
	}
	_, handler := broker.SubscribeTool(&stubBroker{stream: stream}, configured)

	result := call(t, handler, map[string]any{"query": "MATCH", "wait_seconds": float64(60)})
	requireSuccess(t, result)

	got := text(t, result)
	if !strings.Contains(got, "interrupted") {
		t.Errorf("a stream the server closed was reported as a healthy window, got: %s", got)
	}
	if !strings.Contains(got, "closed the stream") {
		t.Errorf("expected the reason to name the close, got: %s", got)
	}
}

// The duration in the summary is what an agent weighs the result by: "nothing
// arrived" means something different over a minute than over a millisecond.
// Reporting the requested window regardless of when the watch ended made every
// early end read as a full, quiet minute.
func TestTheReportedDurationIsTheOneActuallyWatched(t *testing.T) {
	stream := &fakeStream{id: "sub_01hz", err: io.EOF}
	_, handler := broker.SubscribeTool(&stubBroker{stream: stream}, configured)

	result := call(t, handler, map[string]any{"query": "MATCH", "wait_seconds": float64(60)})
	requireSuccess(t, result)

	if got := text(t, result); !strings.Contains(got, "over 0s of a 1m0s window") {
		t.Errorf("expected the watch to be reported as far shorter than the window it asked for, got: %s", got)
	}
}

// A setup failure and an in-flight failure need different remediation, retry
// versus reconnect-and-dedupe, so they must not read the same to the agent.
func TestSetupFailureIsDistinguishedFromAStreamFailure(t *testing.T) {
	stub := &stubBroker{subErr: &semantik.SubscribeSetupError{
		Err: &semantik.Error{Code: semantik.CodeUnavailable, Message: "setup budget exceeded", HTTPStatus: 503},
	}}
	_, handler := broker.SubscribeTool(stub, configured)

	message := requireError(t, call(t, handler, map[string]any{"query": "MATCH"}))

	// Asserted as a prefix rather than a substring, because the marker is the
	// operation name and failure appends " failed" to it. A sentence here instead
	// of a name read as "noetive_subscribe could not start the subscription
	// failed [unavailable]: ...", which marks the failure and mangles it.
	if !strings.HasPrefix(message, "noetive_subscribe (setup) failed") {
		t.Errorf("expected the message to mark this as a setup failure, got: %s", message)
	}
}

// The subscription must be closed when the call returns. A leaked subscription
// keeps consuming server resources for a stream nobody is reading.
func TestSubscriptionIsClosedWhenTheCallReturns(t *testing.T) {
	stream := &fakeStream{id: "sub_01hz", events: []semantik.MatchEvent{{MessageID: "msg_1"}}}
	stub := &stubBroker{stream: stream}
	_, handler := broker.SubscribeTool(stub, configured)

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
	_, handler := broker.SubscribeTool(stub, configured)

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
	_, handler := broker.SubscribeTool(stub, targeting.Policy{})

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
		return semantik.MatchEvent{}, &semantik.SubscribeStreamError{Cause: err}
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
		//
		// The cancellation comes back wrapped, because that is what the real
		// *semantik.Subscription does: wrapSubscribeStreamError wraps any
		// mid-stream error, cancellation included, so the type cannot tell an
		// ended window from a dropped connection. A double that returned the bare
		// context error would let collect distinguish them for free and hide the
		// very defect this shape exists to catch.
		<-ctx.Done()
		return semantik.MatchEvent{}, &semantik.SubscribeStreamError{Cause: ctx.Err()}
	}
	return event, nil
}

func (f *fakeStream) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// A capped call budget has to bind the handler, not only the schema: a client
// that ignores the advertised maximum must still be clamped to the window that
// fits, or the proxy the cap exists for cuts the call off.
func TestACappedBudgetClampsTheWindowAClientAsksFor(t *testing.T) {
	stream := &fakeStream{id: "sub_01hz", events: []semantik.MatchEvent{{MessageID: "msg_1"}}}
	_, handler := broker.SubscribeToolWithin(&stubBroker{stream: stream}, configured, 45*time.Second)

	result := call(t, handler, map[string]any{
		"query":        "MATCH",
		"max_matches":  float64(1),
		"wait_seconds": float64(60),
	})
	requireSuccess(t, result)

	if got := text(t, result); !strings.Contains(got, "25s") {
		t.Errorf("expected the window to be clamped to 25s, got: %s", got)
	}
}

// stallingOpener never finishes setting up: it holds Subscribe until its
// context is cancelled, the way a service that accepts the connection and
// never answers does.
type stallingOpener struct{}

func (stallingOpener) Subscribe(ctx context.Context, _ semantik.SubscribeRequest) (broker.Stream, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Setup has its own budget, and running out of it is reported as that: not as
// the caller cancelling, and not as a broken stream that lost matches. An agent
// reads the difference to decide whether to retry.
func TestASetupThatNeverFinishesIsReportedAsSetupRunningOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 20 second setup budget")
	}
	_, handler := broker.SubscribeToolWithin(stallingOpener{}, configured, 45*time.Second)

	started := time.Now()
	result := call(t, handler, map[string]any{"query": "MATCH", "wait_seconds": float64(25)})
	took := time.Since(started)

	msg := requireError(t, result)
	if !strings.Contains(msg, "setup") || !strings.Contains(msg, "20s") {
		t.Errorf("expected a setup budget refusal naming 20s, got: %s", msg)
	}
	if strings.Contains(msg, "cancelled") {
		t.Errorf("our own budget was reported as the caller leaving: %s", msg)
	}
	if took > 25*time.Second {
		t.Errorf("setup was allowed %s, beyond its 20s budget", took)
	}
}
