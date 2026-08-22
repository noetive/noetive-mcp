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

	got, err := targeting.Policy{Fallback: fallback}.Resolve(call)
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

	got, err := targeting.Policy{Fallback: fallback}.Resolve(call)
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
			_, err := targeting.Policy{}.Resolve(sc.call)

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
	got, err := targeting.Policy{}.Resolve(targeting.Target{Namespace: "incidents"})
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
	if got != (targeting.Policy{}) {
		t.Errorf("expected the zero Policy, got %+v", got)
	}
}

func TestFromEnvReadsEveryVariable(t *testing.T) {
	env := map[string]string{
		targeting.EnvNamespace:     "incidents",
		targeting.EnvModel:         "model-a",
		targeting.EnvDimensions:    "512",
		targeting.EnvDisableGlobal: "1",
	}

	got, err := targeting.FromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}

	want := targeting.Policy{
		Fallback:       targeting.Target{Namespace: "incidents", Model: "model-a", Dimensions: 512},
		GlobalDisabled: true,
	}
	if got != want {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

// Every variable this package reads has to be listed, because the packaging
// emitter checks server.json against this list rather than keeping its own. A
// variable missing here is a variable that silently stops being documented.
func TestEnvNamesCoversEveryVariableRead(t *testing.T) {
	named := map[string]bool{}
	for _, name := range targeting.EnvNames() {
		named[name] = true
	}

	for _, want := range []string{
		targeting.EnvNamespace,
		targeting.EnvModel,
		targeting.EnvDimensions,
		targeting.EnvDisableGlobal,
	} {
		if !named[want] {
			t.Errorf("expected EnvNames to list %s", want)
		}
	}

	// FromEnv reading a variable EnvNames does not report is the failure this
	// catches from the other side: every name reported must be one that a
	// lookup is actually made for.
	var lookedUp []string
	if _, err := targeting.FromEnv(func(k string) string {
		lookedUp = append(lookedUp, k)
		return ""
	}); err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}
	if len(lookedUp) != len(targeting.EnvNames()) {
		t.Errorf("FromEnv looked up %v but EnvNames reports %v", lookedUp, targeting.EnvNames())
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

// The point of the whole feature: with the shared namespace closed, a call that
// resolves to it is refused before any bytes leave the process. If this passes,
// a tenant who closed "global" is still publishing into it.
func TestAClosedGlobalNamespaceIsRefused(t *testing.T) {
	policy := targeting.Policy{
		Fallback:       targeting.Target{Model: "model-a", Dimensions: 512},
		GlobalDisabled: true,
	}

	// Namespace names are case-insensitive, so every spelling below names the
	// same namespace. A guard that matched only the exact lowercase form would
	// leave it reachable by capitalising it, which is a spelling a model
	// produces without being asked.
	for _, spelling := range []string{"global", "Global", "GLOBAL", "  global  ", "gLoBaL"} {
		t.Run(spelling, func(t *testing.T) {
			_, err := policy.Resolve(targeting.Target{Namespace: spelling})

			var forbidden *targeting.ForbiddenError
			if !errors.As(err, &forbidden) {
				t.Fatalf("expected *ForbiddenError for %q, got %v", spelling, err)
			}
		})
	}
}

// The refusal must not extend past the one namespace that was closed. A guard
// that also refuses "globalscope" or "my-global-notes" would break tenants who
// never used the shared namespace at all.
func TestClosingGlobalLeavesEveryOtherNamespaceAlone(t *testing.T) {
	policy := targeting.Policy{
		Fallback:       targeting.Target{Model: "model-a", Dimensions: 512},
		GlobalDisabled: true,
	}

	for _, namespace := range []string{"globalscope", "my-global-notes", "incidents", "global-ops", "acme_global"} {
		t.Run(namespace, func(t *testing.T) {
			got, err := policy.Resolve(targeting.Target{Namespace: namespace})
			if err != nil {
				t.Fatalf("expected %q to be allowed, got: %v", namespace, err)
			}
			if got.Namespace != namespace {
				t.Errorf("expected the namespace to survive unchanged, got %q", got.Namespace)
			}
		})
	}
}

// With the switch off, the shared namespace is an ordinary destination. This is
// the default and the behaviour every existing install depends on.
func TestTheSharedNamespaceIsUsableWhenNotClosed(t *testing.T) {
	policy := targeting.Policy{Fallback: targeting.Target{Model: "model-a", Dimensions: 512}}

	got, err := policy.Resolve(targeting.Target{Namespace: targeting.GlobalNamespace})
	if err != nil {
		t.Fatalf("expected the shared namespace to be usable by default, got: %v", err)
	}
	if got.Namespace != targeting.GlobalNamespace {
		t.Errorf("expected %q, got %q", targeting.GlobalNamespace, got.Namespace)
	}
}

// A configured fallback naming the shared namespace must be refused just as a
// tool call naming it is. Checking only the call argument would let an operator
// close the namespace and keep routing there through their own configuration.
func TestAClosedNamespaceIsRefusedWhenItComesFromTheFallback(t *testing.T) {
	policy := targeting.Policy{
		Fallback:       targeting.Target{Namespace: targeting.GlobalNamespace, Model: "model-a", Dimensions: 512},
		GlobalDisabled: true,
	}

	if _, err := policy.Resolve(targeting.Target{}); err == nil {
		t.Fatal("expected a fallback naming the closed namespace to be refused")
	}
}

// A refused resolution yields the zero Target, so a caller that ignores the
// error publishes nowhere rather than into the namespace that was closed.
func TestARefusedNamespaceYieldsNoTarget(t *testing.T) {
	policy := targeting.Policy{
		Fallback:       targeting.Target{Model: "model-a", Dimensions: 512},
		GlobalDisabled: true,
	}

	got, _ := policy.Resolve(targeting.Target{Namespace: targeting.GlobalNamespace})
	if got != (targeting.Target{}) {
		t.Errorf("expected the zero Target alongside the refusal, got %+v", got)
	}
}

// An operator writes this variable by hand into an editor config. Every
// spelling of yes and no that a person reasonably types has to work, or they
// will believe they closed the namespace when they did not.
func TestTheDisableSwitchAcceptsTheSpellingsPeopleType(t *testing.T) {
	scenarios := []struct {
		raw  string
		want bool
	}{
		{"1", true}, {"true", true}, {"TRUE", true}, {"yes", true}, {"On", true}, {" true ", true},
		{"0", false}, {"false", false}, {"no", false}, {"off", false}, {"", false},
	}

	for _, sc := range scenarios {
		t.Run(sc.raw, func(t *testing.T) {
			got, err := targeting.ParseDisableGlobal(sc.raw)
			if err != nil {
				t.Fatalf("expected %q to parse, got: %v", sc.raw, err)
			}
			if got != sc.want {
				t.Errorf("expected %q to mean %v, got %v", sc.raw, sc.want, got)
			}
		})
	}
}

// A value nobody recognises must stop the process rather than read as false.
// This is the whole reason the variable is parsed strictly: a server that fails
// open here permits exactly what its operator told it to forbid, and says
// nothing.
func TestAnUnrecognisedDisableValueStopsStartup(t *testing.T) {
	for _, raw := range []string{"ture", "enabled", "2", "-1", "y", "disable"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := targeting.ParseDisableGlobal(raw); err == nil {
				t.Errorf("expected %q to be refused rather than read as false", raw)
			}

			env := map[string]string{targeting.EnvDisableGlobal: raw}
			got, err := targeting.FromEnv(func(k string) string { return env[k] })
			if err == nil {
				t.Errorf("expected FromEnv to refuse %q", raw)
			}
			if got != (targeting.Policy{}) {
				t.Errorf("expected the zero Policy alongside the error, got %+v", got)
			}
		})
	}
}

// The refusal message has to say which namespace was refused and what to do
// instead. An agent reads this string and retries; "forbidden" alone would have
// it retry the same call.
func TestTheClosedNamespaceMessageNamesTheNamespaceAndTheRemedy(t *testing.T) {
	err := &targeting.ForbiddenError{Namespace: targeting.GlobalNamespace}

	message := err.Error()
	for _, want := range []string{targeting.GlobalNamespace, targeting.EnvDisableGlobal, "name the namespace"} {
		if !strings.Contains(message, want) {
			t.Errorf("expected the message to mention %q, got: %s", want, message)
		}
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
