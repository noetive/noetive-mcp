// Command noetive-mcp serves the Noetive Semantik broker to an AI editor over
// the Model Context Protocol.
//
// Invoked with no arguments it serves stdio, which is how editors launch it and
// what the "Add to Kiro" deeplink relies on. Configuration of editor config
// files is the job of the npm wrapper, not this binary.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"

	mcpgo "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/mcpserver"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

var version = "dev"

// apiKeyEnv is the one credential the server reads. Named here because the
// message a user sees when it is missing has to spell it exactly.
const apiKeyEnv = "NOETIVE_KEY_SECRET"

func main() {
	// stdio is the protocol channel; anything written to stdout that is not a
	// JSON-RPC frame corrupts the session, so all diagnostics go to stderr.
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	log.SetPrefix("noetive-mcp: ")

	err := run(os.Args[1:], os.Stdout, func(s *mcpgo.MCPServer) error {
		return mcpgo.ServeStdio(s)
	})
	if err != nil {
		log.Fatal(err)
	}
}

// run is the whole program, with the two things a test cannot supply — where
// output goes and what actually serves — passed in.
//
// main keeps only process concerns. Everything a mistake could break lives here
// instead, because the argument handling in particular is load-bearing: the
// bare invocation is how the Add to Kiro deeplink launches the server, and a
// regression there breaks an install path that has already been advertised.
func run(argv []string, stdout io.Writer, serve func(*mcpgo.MCPServer) error) error {
	// ContinueOnError so a bad flag returns here rather than exiting the
	// process out from under a caller.
	flags := flag.NewFlagSet("noetive-mcp", flag.ContinueOnError)
	flags.SetOutput(stdout)

	showVersion := flags.Bool("version", false, "print the version and exit")
	namespace := flags.String("namespace", "", "namespace to route calls to when a tool call does not name one")
	model := flags.String("model", "", "embedding model to use when a tool call does not name one")
	dimensions := flags.Uint("dimensions", 0, "embedding dimensionality to use when a tool call does not name one")

	if err := flags.Parse(withoutServeVerb(argv)); err != nil {
		return err
	}

	if *showVersion {
		// Ignored deliberately: a stdout write that fails has nowhere left to
		// report to, and the exit status already carries the outcome.
		_, _ = fmt.Fprintln(stdout, version)
		return nil
	}

	fallback, err := resolveFallback(*namespace, *model, *dimensions)
	if err != nil {
		return err
	}

	return serve(mcpserver.New(version, connect(), fallback))
}

// withoutServeVerb drops a leading `serve`, which the npm wrapper passes when
// it wants to be explicit.
//
// Both spellings have to reach the same place. The Kiro deeplink launches
// `npx @noetive/mcp-server` with no arguments at all, and every editor config
// written by `init` spawns it the same way, so treating a bare invocation as
// anything other than "serve" breaks every advertised install path.
func withoutServeVerb(argv []string) []string {
	if len(argv) > 0 && argv[0] == "serve" {
		return argv[1:]
	}
	return argv
}

// connect builds the Semantik client, or a broker that explains why it could
// not.
//
// Exiting on a missing credential would leave the editor reporting a server
// that failed to launch, with no tools registered and nothing to ask — not even
// noetive_health, whose whole job is to say what is wrong. Starting degraded
// keeps the tools visible and turns an opaque launch failure into a message the
// agent can read out.
func connect() mcpserver.Broker {
	// Checked before the client is built, because an unexpanded placeholder is
	// a non-empty string that passes every validation the SDK applies. Left
	// alone it reaches the server and returns "unauthorized", pointing the user
	// at their account when the fault is an environment their editor could not
	// read.
	if raw := os.Getenv(apiKeyEnv); mcpserver.PlaceholderKey(raw) {
		return unconfigured("Your editor passed the literal text %q as the API key instead of substituting it, which means %s is not set in the environment your editor was launched from. Desktop launchers do not read your shell profile. Either launch the editor from a terminal where %s is exported, or re-run: npx @noetive/mcp-server init --client <editor> --api-key <key>", raw, apiKeyEnv, apiKeyEnv)
	}

	client, err := semantik.NewFromEnv()
	if err == nil {
		return client
	}

	return unconfigured("%s is not set for this editor. Get an API key from https://noetive.io/dashboard, then either export it in the environment your editor launches from, or re-run: npx @noetive/mcp-server init --client <editor> --api-key <key>", apiKeyEnv)
}

// unconfigured logs the reason once to stderr, where an editor collects server
// logs, and returns a broker that repeats it on every call so the agent can
// read it out too.
func unconfigured(format string, args ...any) mcpserver.Broker {
	reason := fmt.Sprintf(format, args...)
	log.Print(reason)
	return mcpserver.Unconfigured(reason)
}

// resolveFallback layers flags over the environment. Flags win because they are
// written into the editor's own config by the installer and are the more
// specific statement of intent; the environment is the ambient fallback.
//
// The result is deliberately not validated. A server with nothing configured is
// the normal case — the Add to Kiro deeplink launches it with no arguments at
// all — and the missing fields arrive on each tool call, where an omission is
// reported to the agent rather than to a log nobody reads. Refusing to start
// here would leave the editor with no tools and no explanation.
func resolveFallback(namespace, model string, dimensions uint) (targeting.Target, error) {
	fromEnv, err := targeting.FromEnv(os.Getenv)
	if err != nil {
		return targeting.Target{}, err
	}

	if dimensions > math.MaxUint16 {
		return targeting.Target{}, fmt.Errorf("-dimensions must be between 1 and %d, got %d", math.MaxUint16, dimensions)
	}

	return targeting.Layer(
		targeting.Target{Namespace: namespace, Model: model, Dimensions: uint16(dimensions)},
		fromEnv,
	), nil
}
