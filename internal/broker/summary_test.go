package broker_test

import (
	"strings"
	"testing"

	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
)

// The text fallback is the whole result for a client that cannot read
// structured content, so the count has to be right at each boundary — an agent
// told "1 match" when twelve arrived will stop looking.
func TestSearchSummaryCountsMatchesAtEachBoundary(t *testing.T) {
	scenarios := []struct {
		name  string
		hits  int
		wants string
	}{
		{"none", 0, "No matches"},
		{"one", 1, "1 match in"},
		{"several", 12, "12 matches in"},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			results := make([]semantik.ResultItem, sc.hits)
			for i := range results {
				results[i] = semantik.ResultItem{MessageID: "msg", Content: "body"}
			}

			stub := &stubBroker{searchResp: semantik.SearchResponse{Results: results}}
			_, handler := broker.SearchTool(stub, complete)

			got := text(t, call(t, handler, map[string]any{"query": "MATCH"}))
			if !strings.Contains(got, sc.wants) {
				t.Errorf("expected %q in the summary, got: %s", sc.wants, got)
			}
		})
	}
}

// A lint verdict has to distinguish one problem from several, since an agent
// reading only the text decides from this whether to look at the diagnostics.
func TestLintVerdictCountsDiagnostics(t *testing.T) {
	scenarios := []struct {
		name        string
		diagnostics int
		wants       string
	}{
		{"one", 1, "1 diagnostic"},
		{"several", 4, "4 diagnostics"},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			stub := &stubBroker{lintResp: semantik.LintResponse{
				Valid:       false,
				Diagnostics: make([]semantik.LintDiagnostic, sc.diagnostics),
			}}
			_, handler := broker.LintTool(stub)

			got := text(t, call(t, handler, map[string]any{"query": "MATCH"}))
			if !strings.Contains(got, sc.wants) {
				t.Errorf("expected %q, got: %s", sc.wants, got)
			}
		})
	}
}

// A valid query with no normalized form must still report a verdict. Returning
// nothing would read to an agent as a lint that did not run.
func TestValidQueryWithoutANormalizedFormStillReportsAVerdict(t *testing.T) {
	stub := &stubBroker{lintResp: semantik.LintResponse{Valid: true}}
	_, handler := broker.LintTool(stub)

	got := text(t, call(t, handler, map[string]any{"query": "MATCH"}))
	if !strings.Contains(got, "valid") {
		t.Errorf("expected a verdict, got: %s", got)
	}
}

// Each way the watch can end needs a distinct summary, because they mean
// different things: a quiet namespace, a limit the caller set, and a stream that
// broke are three different next actions.
func TestSubscribeSummaryDistinguishesWhyItStopped(t *testing.T) {
	scenarios := []struct {
		name   string
		stream *fakeStream
		args   map[string]any
		wants  string
	}{
		{
			name:   "reached the limit",
			stream: &fakeStream{id: "sub_1", events: []semantik.MatchEvent{{MessageID: "a"}, {MessageID: "b"}}},
			args:   map[string]any{"query": "MATCH", "max_matches": float64(2)},
			wants:  "Stopped at the requested limit",
		},
		{
			name:   "window closed",
			stream: &fakeStream{id: "sub_2"},
			args:   map[string]any{"query": "MATCH", "wait_seconds": float64(1)},
			wants:  "Watched for the full window",
		},
		{
			name: "stream broke",
			stream: &fakeStream{
				id:  "sub_3",
				err: &semantik.SubscribeStreamError{Err: &semantik.Error{Code: semantik.CodeUnavailable}},
			},
			args:  map[string]any{"query": "MATCH", "wait_seconds": float64(5)},
			wants: "interrupted",
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			_, handler := broker.SubscribeTool(&stubBroker{stream: sc.stream}, complete)

			got := text(t, call(t, handler, sc.args))
			if !strings.Contains(got, sc.wants) {
				t.Errorf("expected %q, got: %s", sc.wants, got)
			}
		})
	}
}

// The window and count are clamped at both ends. A zero or negative wait would
// close the window before a single match could arrive, reporting an empty
// namespace that is merely unread.
func TestWatchBoundsAreClampedAtBothEnds(t *testing.T) {
	scenarios := []struct {
		name  string
		args  map[string]any
		wants string
	}{
		{"below the floor", map[string]any{"query": "MATCH", "wait_seconds": float64(0), "max_matches": float64(0)}, "1s"},
		{"above the ceiling", map[string]any{"query": "MATCH", "wait_seconds": float64(9999), "max_matches": float64(1)}, "1m0s"},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			stream := &fakeStream{id: "sub", events: []semantik.MatchEvent{{MessageID: "a"}}}
			_, handler := broker.SubscribeTool(&stubBroker{stream: stream}, complete)

			got := text(t, call(t, handler, sc.args))
			if !strings.Contains(got, sc.wants) {
				t.Errorf("expected the window to be clamped to %q, got: %s", sc.wants, got)
			}
		})
	}
}

// Absent metadata must reach the wire as an empty map, not as a nil the SDK
// would have to guess about.
func TestPublishWithoutMetadataSendsAnEmptyMap(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.PublishTool(stub, complete)

	call(t, handler, map[string]any{"text": "hello"})

	if stub.publishReq.Metadata == nil {
		t.Error("expected an empty metadata map rather than nil")
	}
	if len(stub.publishReq.Metadata) != 0 {
		t.Errorf("expected no metadata entries, got %v", stub.publishReq.Metadata)
	}
}

// Metadata sent as something other than an object must be refused rather than
// ignored: silently dropping it would store a message the agent believes is
// labelled and which no later search can find by that label.
func TestPublishRefusesMetadataThatIsNotAnObject(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.PublishTool(stub, complete)

	requireError(t, call(t, handler, map[string]any{"text": "hello", "metadata": []any{"topic"}}))

	if len(stub.publishReq.Items) != 0 {
		t.Fatal("the broker was called with unusable metadata")
	}
}

// A publish without an explicit ack must leave the field unset so the server
// applies its own default, rather than this client pinning a durability level
// the caller never chose.
func TestOmittedAckLeavesTheServerInCharge(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.PublishTool(stub, complete)

	call(t, handler, map[string]any{"text": "hello"})

	if stub.publishReq.Ack != "" {
		t.Errorf("expected no ack mode to be sent, got %q", stub.publishReq.Ack)
	}
}

// Both durability levels must survive to the wire; silently downgrading a
// durable publish would promise replication the caller never got.
func TestBothAckModesReachTheWire(t *testing.T) {
	for _, mode := range []semantik.AckMode{semantik.AckStored, semantik.AckDurable} {
		t.Run(string(mode), func(t *testing.T) {
			stub := &stubBroker{}
			_, handler := broker.PublishTool(stub, complete)

			call(t, handler, map[string]any{"text": "hello", "ack": string(mode)})

			if stub.publishReq.Ack != mode {
				t.Errorf("expected ack %q, got %q", mode, stub.publishReq.Ack)
			}
		})
	}
}

// A cursor at the very end of the query is the default position for completions
// and must be accepted, not treated as out of range.
func TestACursorAtTheEndOfTheQueryIsAccepted(t *testing.T) {
	stub := &stubBroker{lintResp: semantik.LintResponse{Valid: true}}
	_, handler := broker.LintTool(stub)

	const query = "MATCH"
	result := call(t, handler, map[string]any{"query": query, "cursor": float64(len(query))})

	if result.IsError {
		t.Fatalf("expected a cursor at the end to be accepted, got: %s", text(t, result))
	}
	if stub.lintReq.Cursor != len(query) {
		t.Errorf("expected the cursor to reach the wire, got %d", stub.lintReq.Cursor)
	}
}
