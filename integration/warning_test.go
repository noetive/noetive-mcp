package integration

import (
	"os"
	"strings"
	"testing"
)

// The scheduled workflow is the only reader of the skip reasons, and it reads
// them by grepping the run log. Nothing else ties the two together: reword a
// skip and the grep silently stops matching, leaving a job that quietly never
// warns again: the failure is the absence of a signal, which no run goes red for.
//
// Deliberately untagged, so it runs in the ordinary pull-request job rather than
// only in the scheduled one that would be broken by the drift.
func TestTheScheduledWorkflowStillGrepsTheSkipReasons(t *testing.T) {
	const workflow = "../.github/workflows/scheduled.yml"

	raw, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatalf("could not read %s: %v", workflow, err)
	}

	for _, reason := range []string{brokerDidNotAnswer, riffleNotFound} {
		if !strings.Contains(string(raw), "grep -q '"+reason+"'") {
			t.Errorf("%s no longer greps for %q, so a run that skipped those tests will not warn that it proved nothing", workflow, reason)
		}
	}
}
