package main

import (
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/targeting"
)

// A usable key must produce a real client. If credential resolution ever
// degrades a working setup, every tool silently stops reaching the broker while
// the editor still reports a healthy server.
func TestConnectReturnsALiveClientWhenTheKeyIsUsable(t *testing.T) {
	t.Setenv(apiKeyEnv, "keyu_3xAmPl3Base58Value")

	broker := connect()

	if _, ok := broker.(*semantik.Client); !ok {
		t.Fatalf("expected a live *semantik.Client, got %T", broker)
	}
}

// The placeholder trap: an editor that does not substitute its config variable
// passes the literal text through. It is non-empty, so nothing downstream
// rejects it, and the call reaches the server and returns "unauthorized" —
// sending the user to check their account when the fault is an environment
// their editor never read.
func TestConnectRefusesAnUnexpandedPlaceholderBeforeBuildingAClient(t *testing.T) {
	t.Setenv(apiKeyEnv, "${NOETIVE_KEY_SECRET}")

	broker := connect()

	if _, ok := broker.(*semantik.Client); ok {
		t.Fatal("a placeholder was accepted as a working credential")
	}

	err := broker.Health(context.Background())
	if err == nil {
		t.Fatal("expected every call to be refused")
	}
	if !strings.Contains(err.Error(), apiKeyEnv) {
		t.Errorf("expected the refusal to name %s, got: %v", apiKeyEnv, err)
	}
	if !strings.Contains(err.Error(), "substituting") {
		t.Errorf("expected the refusal to explain the substitution failure, got: %v", err)
	}
}

// With no key at all the server must still start and still answer, so the
// editor shows tools and the agent can read out what is wrong. Exiting instead
// leaves only "server failed to launch".
func TestConnectDegradesRatherThanFailingWhenNoKeyIsSet(t *testing.T) {
	t.Setenv(apiKeyEnv, "")

	err := connect().Health(context.Background())
	if err == nil {
		t.Fatal("expected calls to be refused with no credential")
	}
	if !strings.Contains(err.Error(), "noetive.io/dashboard") {
		t.Errorf("expected the refusal to say where to get a key, got: %v", err)
	}
}

// Flags are written into the editor's own config by the installer and are the
// more specific statement of intent, so they win over the ambient environment.
func TestFlagsOverrideTheEnvironment(t *testing.T) {
	t.Setenv(targeting.EnvNamespace, "from-env")
	t.Setenv(targeting.EnvModel, "model-env")
	t.Setenv(targeting.EnvDimensions, "512")

	got, err := resolveFallback("from-flag", "", 0)
	if err != nil {
		t.Fatalf("resolveFallback returned error: %v", err)
	}

	want := targeting.Target{Namespace: "from-flag", Model: "model-env", Dimensions: 512}
	if got != want {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

// Startup must tolerate a completely unconfigured server: that is how the Add
// to Kiro deeplink launches it, with no arguments and no environment. Refusing
// here would leave the editor with no tools and no explanation.
func TestStartupSucceedsWithNothingConfigured(t *testing.T) {
	t.Setenv(targeting.EnvNamespace, "")
	t.Setenv(targeting.EnvModel, "")
	t.Setenv(targeting.EnvDimensions, "")

	got, err := resolveFallback("", "", 0)
	if err != nil {
		t.Fatalf("expected an unconfigured server to start, got: %v", err)
	}
	if got != (targeting.Target{}) {
		t.Errorf("expected an empty fallback, got %+v", got)
	}
}

// A dimensionality outside uint16 must be refused rather than truncated:
// wrapping 65537 to 1 would start the server with a plausible-looking but wrong
// value that only surfaces as a server-side rejection much later.
func TestOutOfRangeDimensionsAreRefusedAtStartup(t *testing.T) {
	if _, err := resolveFallback("", "", 70000); err == nil {
		t.Fatal("expected out-of-range dimensions to be refused")
	}
}

// A malformed environment value must stop startup rather than being discarded,
// because a silently-dropped dimensionality reappears as an opaque
// model_not_provisioned error from the server.
func TestUnparseableEnvironmentDimensionsStopStartup(t *testing.T) {
	t.Setenv(targeting.EnvDimensions, "1024d")

	if _, err := resolveFallback("", "", 0); err == nil {
		t.Fatal("expected an unparseable dimensions value to be refused")
	}
}

// The bare invocation and the explicit `serve` verb must reach the same place.
// The Add to Kiro deeplink launches this with no arguments at all, and every
// editor config written by `init` spawns it the same way, so a regression here
// breaks install paths that are already advertised in public.
func TestBareAndExplicitInvocationsBothServe(t *testing.T) {
	t.Setenv(apiKeyEnv, "keyu_3xAmPl3Base58Value")

	scenarios := []struct {
		name string
		argv []string
	}{
		{"bare, as the Kiro deeplink launches it", nil},
		{"explicit serve, as the npm wrapper spells it", []string{"serve"}},
		{"serve with flags after it", []string{"serve", "-namespace", "incidents"}},
		{"flags with no verb", []string{"-namespace", "incidents"}},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			served := false
			err := run(sc.argv, io.Discard, func(*mcpgo.MCPServer) error {
				served = true
				return nil
			})
			if err != nil {
				t.Fatalf("run returned error: %v", err)
			}
			if !served {
				t.Error("the server was never served")
			}
		})
	}
}

// Only a leading `serve` is a verb. Stripping the first argument
// unconditionally would silently swallow a flag and start a server configured
// differently from what the user asked for.
func TestOnlyALeadingServeVerbIsStripped(t *testing.T) {
	scenarios := []struct {
		name string
		argv []string
		want []string
	}{
		{"no arguments", nil, nil},
		{"leading verb", []string{"serve", "-namespace", "x"}, []string{"-namespace", "x"}},
		{"verb only", []string{"serve"}, []string{}},
		{"flag first", []string{"-namespace", "x"}, []string{"-namespace", "x"}},
		{"serve as a flag value is not a verb", []string{"-namespace", "serve"}, []string{"-namespace", "serve"}},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			got := withoutServeVerb(sc.argv)
			if len(got) != len(sc.want) {
				t.Fatalf("expected %v, got %v", sc.want, got)
			}
			for i := range got {
				if got[i] != sc.want[i] {
					t.Errorf("expected %v, got %v", sc.want, got)
				}
			}
		})
	}
}

// --version must be opt-in. Defaulted the other way, every editor launch prints
// a version string to stdout and exits — which for a stdio protocol means the
// editor sees a corrupt frame and a server that immediately died.
func TestVersionIsOnlyPrintedWhenAsked(t *testing.T) {
	t.Setenv(apiKeyEnv, "keyu_3xAmPl3Base58Value")

	var asked strings.Builder
	served := false
	if err := run([]string{"-version"}, &asked, func(*mcpgo.MCPServer) error {
		served = true
		return nil
	}); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	if served {
		t.Error("--version served instead of printing and returning")
	}
	if !strings.Contains(asked.String(), version) {
		t.Errorf("expected the version on stdout, got %q", asked.String())
	}

	var unasked strings.Builder
	if err := run(nil, &unasked, func(*mcpgo.MCPServer) error { return nil }); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if unasked.Len() != 0 {
		t.Errorf("a normal launch wrote %q to stdout, which corrupts the protocol stream", unasked.String())
	}
}

// An unusable flag must stop startup rather than being ignored, or the server
// runs with configuration the user believes they changed.
func TestAnUnknownFlagStopsStartup(t *testing.T) {
	err := run([]string{"-nonsense"}, io.Discard, func(*mcpgo.MCPServer) error {
		t.Error("the server was served despite an unusable flag")
		return nil
	})
	if err == nil {
		t.Fatal("expected an unknown flag to be refused")
	}
}

// A configuration error must stop startup before anything serves.
func TestAConfigurationErrorPreventsServing(t *testing.T) {
	t.Setenv(targeting.EnvDimensions, "not-a-number")

	err := run(nil, io.Discard, func(*mcpgo.MCPServer) error {
		t.Error("the server was served despite unusable configuration")
		return nil
	})
	if err == nil {
		t.Fatal("expected unusable configuration to stop startup")
	}
}

// A failure from the transport has to reach the caller, or the process exits
// zero on a server that never ran and the editor reports nothing at all.
func TestAServeFailureIsReturned(t *testing.T) {
	t.Setenv(apiKeyEnv, "keyu_3xAmPl3Base58Value")

	sentinel := errors.New("stdio closed")
	err := run(nil, io.Discard, func(*mcpgo.MCPServer) error { return sentinel })

	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the transport error, got %v", err)
	}
}

// The largest usable dimensionality must be accepted. Rejecting at the boundary
// would refuse a legitimate model for being exactly as large as allowed.
func TestTheLargestUsableDimensionalityIsAccepted(t *testing.T) {
	got, err := resolveFallback("", "", math.MaxUint16)
	if err != nil {
		t.Fatalf("expected %d dimensions to be accepted, got: %v", math.MaxUint16, err)
	}
	if got.Dimensions != math.MaxUint16 {
		t.Errorf("expected the value to survive, got %d", got.Dimensions)
	}
}
