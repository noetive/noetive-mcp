package broker_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
)

// Step-0 reproduction for D3: a quiet window is reported as a broken stream.
//
// All four live subscribe calls in the MCP test report came back interrupted
// with zero matches, against a server whose subscribe path is correct. The cause
// is here, in how the collect window ends.
//
// The existing fakeStream honours the per-read context it is handed, so the
// window appears to close cleanly and every test passes. The real
// *semantik.Subscription does the opposite: Subscription.Next documents that it
// samples its context once on entry and then ignores it, because
// bufio.Scanner.Scan blocks and only the context passed to Client.Subscribe can
// unblock it. In production the per-read deadline therefore fires against
// nobody, the read stays blocked until the shared stream context expires, and
// the SDK wraps that DeadlineExceeded in a *SubscribeStreamError — which the
// tool classifies as an interruption.
//
// idleStream below is written to the contract the SDK actually has. Correcting
// the double is as much a part of this fix as the code change: a test cannot
// catch this while its stand-in behaves better than the real thing.

// idleStream models a subscription over a namespace where nothing is published.
// It ignores the per-read context exactly as Subscription.Next does, blocking
// until the stream context is cancelled and then reporting that cancellation the
// way the SDK does — wrapped, so it is indistinguishable by type from a genuine
// mid-stream failure.
type idleStream struct {
	streamCtx context.Context
	id        string

	mu     sync.Mutex
	closed bool
}

func (s *idleStream) ID() string { return s.id }

func (s *idleStream) Next(context.Context) (semantik.MatchEvent, error) {
	<-s.streamCtx.Done()
	return semantik.MatchEvent{}, &semantik.SubscribeStreamError{Cause: s.streamCtx.Err()}
}

func (s *idleStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// idleBroker hands back a stream bound to the very context the tool passed to
// Subscribe, which is what makes the stream's lifetime observable to the test.
type idleBroker struct{}

func (idleBroker) Subscribe(ctx context.Context, _ semantik.SubscribeRequest) (broker.Stream, error) {
	return &idleStream{streamCtx: ctx, id: "sub_idle"}, nil
}

// TestSubscribeIdleWindowIsNotAnInterruption is the core D3 reproduction.
//
// Watching a quiet namespace for the full window and seeing nothing is the
// ordinary outcome — subscribe is live-only and makes no replay promise, so a
// standalone watch usually ends exactly this way. Reporting it as an
// interruption tells an agent the transport failed when it did not, and the
// advice that follows it ("matches after the interruption are lost") describes
// an event that never happened.
func TestSubscribeIdleWindowIsNotAnInterruption(t *testing.T) {
	_, handler := broker.SubscribeTool(idleBroker{}, configured)

	result := call(t, handler, map[string]any{"query": "MATCH", "wait_seconds": float64(1)})
	requireSuccess(t, result)

	got := text(t, result)
	if strings.Contains(got, "interrupted") {
		t.Errorf("a quiet window was reported as an interrupted stream; the window simply closed. got: %s", got)
	}
	if !strings.Contains(got, "0 matches") {
		t.Errorf("expected an empty but healthy window, got: %s", got)
	}
}

// TestSubscribeHonoursTheRequestedWindow pins the second half of the same
// defect.
//
// The per-read deadline is inert against the real stream, so what actually ends
// the window is the stream context — which the handler sizes at
// setupBudget + wait. A one-second watch therefore blocks the agent's turn for
// twenty-one seconds, whether or not setup took any time at all. The budget is
// meant to cover setup, not to be added to every window.
func TestSubscribeHonoursTheRequestedWindow(t *testing.T) {
	_, handler := broker.SubscribeTool(idleBroker{}, configured)

	start := time.Now()
	requireSuccess(t, call(t, handler, map[string]any{"query": "MATCH", "wait_seconds": float64(1)}))
	elapsed := time.Since(start)

	// Generous margin: the claim is that the requested window bounds the call at
	// all, not that it is precise.
	if elapsed > 5*time.Second {
		t.Errorf("a 1s watch took %s; the requested window must bound the call rather than the setup budget being added to it", elapsed)
	}
}

// TestSubscribeGenuineStreamFailureIsStillInterrupted bounds the fix, so it
// cannot be satisfied by never reporting an interruption at all. A stream that
// really breaks mid-read still has to be surfaced — otherwise a false alarm has
// merely been traded for a silent one.
func TestSubscribeGenuineStreamFailureIsStillInterrupted(t *testing.T) {
	stream := &fakeStream{
		id:     "sub_broken",
		events: []semantik.MatchEvent{{MessageID: "msg_1", Score: 0.9}},
		err:    &semantik.SubscribeStreamError{Cause: context.Canceled},
	}
	_, handler := broker.SubscribeTool(&stubBroker{stream: stream}, configured)

	result := call(t, handler, map[string]any{"query": "MATCH", "wait_seconds": float64(60)})
	requireSuccess(t, result)

	got := text(t, result)
	if !strings.Contains(got, "interrupted") {
		t.Errorf("a genuine mid-stream failure was not reported as interrupted, got: %s", got)
	}
	if !strings.Contains(got, "1 matches") {
		t.Errorf("expected the match that arrived before the break to survive, got: %s", got)
	}
}
