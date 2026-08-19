package broker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
)

// Health exists so a user can tell "cannot reach Noetive" apart from "the query
// was wrong". A reachable broker must say so plainly.
func TestHealthReportsAReachableBroker(t *testing.T) {
	_, handler := broker.HealthTool(&stubBroker{})

	result := call(t, handler, map[string]any{})
	if result.IsError {
		t.Fatalf("expected success, got: %s", text(t, result))
	}
	if !strings.Contains(text(t, result), "reachable") {
		t.Errorf("expected a plain verdict, got: %s", text(t, result))
	}
}

// A rejected key is the single most common setup failure. The code has to
// survive so the remediation ("your key is wrong") is distinguishable from an
// outage ("try again later").
func TestRejectedKeyIsReportedWithItsCode(t *testing.T) {
	_, handler := broker.HealthTool(&stubBroker{healthErr: &semantik.Error{
		Code:       semantik.CodeUnauthorized,
		Message:    "api key not accepted",
		HTTPStatus: 401,
	}})

	message := requireError(t, call(t, handler, map[string]any{}))

	if !strings.Contains(message, semantik.CodeUnauthorized) {
		t.Errorf("expected the code in the message, got: %s", message)
	}
}

// A failed probe must never read as a reachable broker. This is the assertion
// the doctor skill leans on: if health can claim success while the call failed,
// every downstream diagnosis starts from a false premise.
func TestUnreachableBrokerIsNeverReportedAsReachable(t *testing.T) {
	_, handler := broker.HealthTool(&stubBroker{healthErr: context.DeadlineExceeded})

	message := requireError(t, call(t, handler, map[string]any{}))

	if strings.Contains(message, "reachable and the API key was accepted") {
		t.Fatalf("a failed probe claimed success: %s", message)
	}
}
