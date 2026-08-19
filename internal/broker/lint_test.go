package broker_test

import (
	"strings"
	"testing"

	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
)

// Lint is the one tool an agent can call before it knows where it is writing,
// which is what makes it useful for repairing a query after an invalid_request.
// Requiring a namespace here would defeat that.
func TestLintNeedsNoNamespace(t *testing.T) {
	stub := &stubBroker{lintResp: semantik.LintResponse{Valid: true}}
	_, handler := broker.LintTool(stub)

	result := call(t, handler, map[string]any{"query": "MATCH DISTANCE(\"x\") WITHIN 0.4"})
	requireSuccess(t, result)

	if stub.lintReq.Query == "" {
		t.Fatal("expected the query to reach the broker")
	}
}

// A valid verdict has to be legible in the text fallback, not only in
// structured content, since not every client reads structured results.
func TestValidQueryIsReportedInWords(t *testing.T) {
	stub := &stubBroker{lintResp: semantik.LintResponse{Valid: true, Normalized: "MATCH DISTANCE(\"x\") WITHIN 0.4"}}
	_, handler := broker.LintTool(stub)

	result := call(t, handler, map[string]any{"query": "match distance(\"x\") within 0.4"})
	requireSuccess(t, result)

	got := text(t, result)
	if !strings.Contains(got, "valid") {
		t.Errorf("expected a verdict in the text fallback, got: %s", got)
	}
	if !strings.Contains(got, "MATCH DISTANCE") {
		t.Errorf("expected the normalized form in the text fallback, got: %s", got)
	}
}

// An invalid query is a successful lint, not a failed one: the tool did its job.
// Reporting it as an error would tell the agent the linter is broken.
func TestInvalidQueryIsASuccessfulLint(t *testing.T) {
	stub := &stubBroker{lintResp: semantik.LintResponse{
		Valid:       false,
		Diagnostics: []semantik.LintDiagnostic{{}, {}},
	}}
	_, handler := broker.LintTool(stub)

	result := call(t, handler, map[string]any{"query": "MATCH WITHIN"})
	requireSuccess(t, result)

	if got := text(t, result); !strings.Contains(got, "2 diagnostics") {
		t.Errorf("expected the diagnostic count, got: %s", got)
	}
}

// A cursor past the end of the query is a caller mistake that the server would
// reject opaquely; catching it here says which argument is wrong.
func TestCursorOutsideTheQueryIsRefused(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.LintTool(stub)

	scenarios := []struct {
		name   string
		cursor float64
	}{
		{"past the end", 99},
		{"negative", -1},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			stub.lintReq = semantik.LintRequest{}
			requireError(t, call(t, handler, map[string]any{"query": "MATCH", "cursor": sc.cursor}))

			if stub.lintReq.Query != "" {
				t.Fatal("the broker was called with an out-of-range cursor")
			}
		})
	}
}
