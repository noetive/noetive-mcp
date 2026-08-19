package targeting_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/noetive/noetive-mcp/internal/targeting"
)

// A call that names every field must be routed exactly as asked, with no
// configured value bleeding in. This is the baseline guarantee: what the agent
// asked for is what goes on the wire.
func TestCallValuesWinOverConfiguration(t *testing.T) {
	call := targeting.Target{Namespace: "incidents", Model: "model-a", Dimensions: 512}
	fallback := targeting.Target{Namespace: "global", Model: "model-b", Dimensions: 1024}

	got, err := targeting.Resolve(call, fallback)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got != call {
		t.Errorf("expected %+v, got %+v", call, got)
	}
}

// Resolution is per-field so an operator can pin a namespace while leaving the
// model to the caller. All-or-nothing fallback would force operators to
// configure fields they have no opinion about.
func TestUnsetFieldsFallBackIndividually(t *testing.T) {
	call := targeting.Target{Namespace: "incidents"}
	fallback := targeting.Target{Namespace: "global", Model: "model-b", Dimensions: 1024}

	got, err := targeting.Resolve(call, fallback)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	want := targeting.Target{Namespace: "incidents", Model: "model-b", Dimensions: 1024}
	if got != want {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

// The central data-isolation guarantee: a field nobody named is an error, never
// a substituted value. If this ever passes, a forgotten namespace becomes a
// silent write into someone else's space.
func TestMissingFieldIsRefusedAndNamed(t *testing.T) {
	complete := targeting.Target{Namespace: "incidents", Model: "model-a", Dimensions: 512}

	scenarios := []struct {
		name  string
		call  targeting.Target
		field string
	}{
		{"namespace", targeting.Target{Model: complete.Model, Dimensions: complete.Dimensions}, "namespace"},
		{"model", targeting.Target{Namespace: complete.Namespace, Dimensions: complete.Dimensions}, "model"},
		{"dimensions", targeting.Target{Namespace: complete.Namespace, Model: complete.Model}, "dimensions"},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			_, err := targeting.Resolve(sc.call, targeting.Target{})

			var missing *targeting.MissingError
			if !errors.As(err, &missing) {
				t.Fatalf("expected *MissingError, got %v", err)
			}
			if missing.Field != sc.field {
				t.Errorf("expected the error to name %q, got %q", sc.field, missing.Field)
			}
		})
	}
}

// A refused resolution must yield the zero Target, not a half-filled one. A
// caller that ignores the error would otherwise publish into whatever partial
// namespace survived.
func TestRefusedResolutionYieldsNoTarget(t *testing.T) {
	got, err := targeting.Resolve(targeting.Target{Namespace: "incidents"}, targeting.Target{})
	if err == nil {
		t.Fatal("expected an error for a partially-specified target")
	}
	if got != (targeting.Target{}) {
		t.Errorf("expected the zero Target alongside the error, got %+v", got)
	}
}

// Layer is what startup uses, and startup must tolerate an incomplete result.
// Validating there would refuse to start a server with nothing configured —
// which is exactly how the Add to Kiro deeplink launches it, leaving the editor
// with no tools and no explanation.
func TestLayerAcceptsAnIncompleteResult(t *testing.T) {
	got := targeting.Layer(targeting.Target{Namespace: "incidents"}, targeting.Target{})

	if got.Namespace != "incidents" {
		t.Errorf("expected the namespace to survive, got %q", got.Namespace)
	}
	if got.Model != "" || got.Dimensions != 0 {
		t.Errorf("expected the unset fields to stay unset, got %+v", got)
	}
}

// Layer must merge in the same direction as Resolve, or a flag would lose to an
// environment variable at startup while winning at call time.
func TestLayerPrefersTheOverlay(t *testing.T) {
	got := targeting.Layer(
		targeting.Target{Namespace: "from-flag"},
		targeting.Target{Namespace: "from-env", Model: "model-b", Dimensions: 1024},
	)

	want := targeting.Target{Namespace: "from-flag", Model: "model-b", Dimensions: 1024}
	if got != want {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

// An empty environment is the normal case for the bare `npx @noetive/mcp-server`
// invocation the Kiro deeplink uses: nothing is configured and every field
// arrives on the tool call.
func TestEmptyEnvironmentIsNotAnError(t *testing.T) {
	got, err := targeting.FromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}
	if got != (targeting.Target{}) {
		t.Errorf("expected the zero Target, got %+v", got)
	}
}

func TestFromEnvReadsAllThreeFields(t *testing.T) {
	env := map[string]string{
		targeting.EnvNamespace:  "incidents",
		targeting.EnvModel:      "model-a",
		targeting.EnvDimensions: "512",
	}

	got, err := targeting.FromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}

	want := targeting.Target{Namespace: "incidents", Model: "model-a", Dimensions: 512}
	if got != want {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

// A typo in the dimensions variable must stop the process rather than being
// discarded, because a silently-dropped dimensionality reappears much later as
// an opaque model_not_provisioned error from the server.
func TestUnusableDimensionsAreRejected(t *testing.T) {
	scenarios := []struct{ name, value string }{
		{"not a number", "1024d"},
		{"negative", "-1"},
		{"above uint16", "70000"},
		{"zero", "0"},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			env := map[string]string{targeting.EnvDimensions: sc.value}
			if _, err := targeting.FromEnv(func(k string) string { return env[k] }); err == nil {
				t.Errorf("expected %q to be rejected", sc.value)
			}
		})
	}
}

// The message is what a user actually sees when a call is refused, so it has to
// name the missing field and say both ways to supply it. A generic "invalid
// request" would leave them guessing which of three fields was wrong.
func TestTheRefusalMessageNamesTheFieldAndBothRemedies(t *testing.T) {
	err := &targeting.MissingError{Field: "namespace"}

	message := err.Error()
	for _, want := range []string{"namespace", "tool call", "server"} {
		if !strings.Contains(message, want) {
			t.Errorf("expected the message to mention %q, got: %s", want, message)
		}
	}
}
