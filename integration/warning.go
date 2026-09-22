// Package integration drives the assembled MCP server against the live Noetive
// Semantik API. The suite itself is behind the "integration" build tag; this
// file is not, so the contract below is checked by the ordinary test run.
package integration

// brokerDidNotAnswer opens the reason every test gives for skipping when the
// failure was the broker's rather than this client's.
//
// The scheduled workflow greps the run log for it. A run where every
// text-carrying test skipped is a run that proved nothing, and this phrase is
// the only thing that distinguishes it from a clean pass: go test reports both
// as exit zero.
//
// A constant rather than a literal in two places, because the coupling fails
// silently in the direction that matters: reword the skip and the grep stops
// matching, with no compile error and no red run to notice it by. warning_test
// asserts the workflow still contains this value.
const brokerDidNotAnswer = "the broker did not answer"
