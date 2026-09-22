package integration

import (
	"os"
	"strings"
	"testing"
)

// The scheduled workflow is the only reader of the skip reason, and it reads it
// by grepping the run log. Nothing else ties the two together: reword the skip
// and the grep silently stops matching, leaving a job that quietly never warns
// again: the failure is the absence of a signal, which no run goes red for.
//
// Deliberately untagged, so it runs in the ordinary pull-request job rather than
// only in the scheduled one that would be broken by the drift.
func TestTheScheduledWorkflowStillGrepsTheSkipReason(t *testing.T) {
	const workflow = "../.github/workflows/scheduled.yml"

	raw, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatalf("could not read %s: %v", workflow, err)
	}

	if !strings.Contains(string(raw), "grep -q '"+brokerDidNotAnswer+"'") {
		t.Errorf("%s no longer greps for %q, so a run that skipped every test will not warn that it proved nothing", workflow, brokerDidNotAnswer)
	}
}
